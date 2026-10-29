//go:build lab

package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type kindAsset struct {
	ID, State, Sha256 string
	Bytes             int64
}
type kindUpload struct {
	Asset         kindAsset
	UploadSession struct {
		ID, State                    string
		ReceivedBytes, ExpectedBytes int64
	}
}
type kindAssetRecord struct {
	StorageID string `json:"storage_upload_id"`
	Parts     []struct {
		Number int32  `json:"number"`
		ETag   string `json:"etag"`
	} `json:"storage_parts"`
	Owner         string `json:"owner_id"`
	State         string `json:"state"`
	SessionState  string `json:"session_state"`
	Staging       string `json:"staging_key"`
	Final         string `json:"final_key"`
	Actual        string `json:"actual_sha256"`
	Received      int64  `json:"received_bytes"`
	AssetReceived int64  `json:"asset_received"`
	Expected      int64  `json:"expected_bytes"`
	Cleaned       bool   `json:"cleaned"`
}

func kindDecode(t *testing.T, data []byte, dst any) {
	t.Helper()
	if err := json.Unmarshal(data, dst); err != nil {
		t.Fatal("invalid deployed JSON response")
	}
}

func (l *kindLab) assetRecord(id string) kindAssetRecord {
	l.t.Helper()
	var row kindAssetRecord
	kindDecode(l.t, []byte(l.sql(fmt.Sprintf(`SELECT row_to_json(r) FROM (
SELECT a.owner_id,a.state,s.state AS session_state,a.staging_key,a.final_key,
encode(a.actual_sha256,'hex') AS actual_sha256,s.received_bytes,
a.received_bytes AS asset_received,a.expected_bytes,a.storage_upload_id,a.storage_parts,s.storage_cleaned_at IS NOT NULL AS cleaned
FROM asset.assets a JOIN asset.upload_sessions s ON s.asset_id=a.id WHERE a.id='%s') r`, id))), &row)
	if row.Owner == "" {
		l.t.Fatal("missing persisted Asset record")
	}
	return row
}

// Clone the deployed gateway image/config without modifying any Deployment.
// Only ASSET_TARGET is pinned to a real Asset Pod IP. An unsatisfied readiness
// gate keeps these probe Pods OUT of normal gateway Service endpoints, while
// preserving the real gateway NetworkPolicy, certificates, Identity and Content.
// Agent Self-Claimed: deterministic routing, not random balancing as evidence.
func (l *kindLab) pinnedGateway(name, assetIP string) *http.Client {
	return l.pinnedGatewayTarget(name, "ASSET_TARGET", assetIP)
}

// Positive cross-Pod checks only; outage/readiness checks use normal NodePort.
func (l *kindLab) pinnedGatewayTarget(name, target, ip string) *http.Client {
	l.t.Helper()
	var deployment struct {
		Spec struct {
			Template struct {
				Spec map[string]any
			}
		}
	}
	kindDecode(l.t, l.kube("", "get", "deployment", "gateway", "-o", "json"), &deployment)
	spec := deployment.Spec.Template.Spec
	container := spec["containers"].([]any)[0].(map[string]any)
	found := false
	for _, item := range container["env"].([]any) {
		env := item.(map[string]any)
		if env["name"] == target {
			env["value"] = net.JoinHostPort(ip, "8443")
			found = true
		}
	}
	if !found {
		l.t.Fatal("deployed gateway has no " + target)
	}
	spec["readinessGates"] = []any{map[string]any{"conditionType": "human-worth.io/isolated-verification"}}
	pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": l.namespace, "labels": map[string]string{"app": "gateway"}}, "spec": spec}
	data, err := json.Marshal(pod)
	if err != nil {
		l.t.Fatal(err)
	}
	l.kube(string(data), "create", "-f", "-")
	l.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		if err := l.command(ctx, "", "delete", "pod", name, "--ignore-not-found", "--wait=true").Run(); err != nil {
			l.t.Error("could not clean up pinned gateway Pod")
		}
	})
	l.kube("", "wait", "--for=condition=ContainersReady", "pod/"+name, "--timeout=120s")
	return l.httpClient(l.forward("pod/"+name, 8080))
}

func TestKindAssetDeployment(t *testing.T) {
	l := newKindLab(t)
	alice, bob := l.user(), l.user()
	var pods struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     struct{ NodeName string }
			Status   struct{ PodIP string }
		}
	}
	kindDecode(t, l.kube("", "get", "pods", "-l", "app=asset", "-o", "json"), &pods)
	if len(pods.Items) != 2 || pods.Items[0].Spec.NodeName == pods.Items[1].Spec.NodeName || pods.Items[0].Status.PodIP == pods.Items[1].Status.PodIP {
		t.Fatal("expected two distinct Asset Pods on different worker nodes")
	}
	a, b := pods.Items[0], pods.Items[1]
	writer := l.pinnedGateway("asset-verify-writer", a.Status.PodIP)
	reader := l.pinnedGateway("asset-verify-reader", b.Status.PodIP)
	t.Logf("real Gateway routes pinned to Asset writer=%s reader=%s on distinct workers", a.Metadata.Name, b.Metadata.Name)

	// Observer only: tunnel to this namespace's real S3 store. Application
	// traffic still follows its manifests' NetworkPolicies; no policy is widened.
	cfgData, err := os.ReadFile(filepath.Join(l.state, "asset-s3.json"))
	if err != nil {
		t.Fatal("private verification S3 config unavailable")
	}
	var cfg asset.S3Config
	kindDecode(t, cfgData, &cfg)
	cfg.Endpoint = "http://127.0.0.1:" + l.forward("service/asset-store", 8333)
	store, err := asset.NewS3Storage(cfg)
	if err != nil {
		t.Fatal("verification S3 observer initialization failed")
	}
	if err = asset.CheckStorage(t.Context(), store); err != nil {
		t.Fatal("verification bucket unavailable")
	}

	remote := s3.New(s3.Options{Region: cfg.Region, BaseEndpoint: aws.String(cfg.Endpoint), UsePathStyle: cfg.ForcePathStyle, RetryMaxAttempts: 1, Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""), HTTPClient: &http.Client{Timeout: 30 * time.Second}})
	assertMultipart := func(row kindAssetRecord, expected int) {
		t.Helper()
		result, err := remote.ListParts(t.Context(), &s3.ListPartsInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(row.Staging), UploadId: aws.String(row.StorageID)})
		if err != nil || len(result.Parts) != 1 || len(row.Parts) != 1 || aws.ToInt64(result.Parts[0].Size) != int64(expected) || row.Parts[0].ETag != aws.ToString(result.Parts[0].ETag) {
			t.Fatal("persisted multipart progress differs from shared S3", err)
		}
		if _, err = store.Stat(t.Context(), row.Staging); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("unfinished multipart became visible", err)
		}
	}
	data := bytes.Repeat([]byte("a"), platform.AssetUploadChunkBytes+511) // One full 5 MiB part and a final remainder.
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	body := fmt.Sprintf(`{"filename":"private.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%s"}`, len(data), digest)
	create := func(client *http.Client, key string) kindUpload {
		t.Helper()
		var result kindUpload
		kindDecode(t, l.request(client, alice, "POST", "/api/me/assets/upload-sessions", body, map[string]string{"Idempotency-Key": key}, 201), &result)
		if result.Asset.ID == "" || result.UploadSession.ID == "" || result.Asset.State != "staging" || result.UploadSession.State != "active" || result.UploadSession.ExpectedBytes != int64(len(data)) {
			t.Fatal("invalid deployed upload session")
		}
		return result
	}
	upload := func(client *http.Client, user kindUser, created kindUpload, offset, end int, expected ...int) []byte {
		t.Helper()
		headers := map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes %d-%d/%d", offset, end-1, len(data))}
		return l.request(client, user, "PUT", "/api/me/assets/upload-sessions/"+created.UploadSession.ID+"/content", string(data[offset:end]), headers, expected...)
	}
	complete := func(client *http.Client, user kindUser, created kindUpload, expected int) []byte {
		t.Helper()
		return l.request(client, user, "POST", "/api/me/assets/"+created.Asset.ID+"/complete", `{"uploadSessionId":"`+created.UploadSession.ID+`"}`, nil, expected)
	}
	assertObject := func(key string, expected []byte) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		size, e := store.Stat(ctx, key)
		if e != nil || size != int64(len(expected)) {
			t.Fatal("unexpected S3 object size/state")
		}
		object, e := store.Open(ctx, key)
		if e != nil {
			t.Fatal("cannot read verification S3 object")
		}
		got, e := io.ReadAll(io.LimitReader(object, int64(len(expected)+1)))
		object.Close()
		if e != nil || !bytes.Equal(got, expected) {
			t.Fatal("S3 content differs")
		}
	}
	assertMissing := func(key string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		if _, e := store.Stat(ctx, key); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("S3 object was not removed (or store unavailable)")
		}
	}
	assertReady := func(created kindUpload) kindAssetRecord {
		t.Helper()
		row := l.assetRecord(created.Asset.ID)
		if row.State != "ready" || row.SessionState != "completed" || row.Owner != alice.account || row.Received != int64(len(data)) || row.AssetReceived != row.Received || row.Expected != row.Received || row.Actual != digest {
			t.Fatal("persisted Asset readiness/owner/size/digest mismatch")
		}
		return row
	}
	assertDownload := func(client *http.Client, created kindUpload) {
		t.Helper()
		got := l.request(client, alice, "GET", "/api/me/assets/"+created.Asset.ID+"/content", "", nil, 200)
		if !bytes.Equal(got, data) || fmt.Sprintf("%x", sha256.Sum256(got)) != digest {
			t.Fatal("cross-Pod download length/content/SHA-256 mismatch")
		}
	}

	l.request(writer, kindUser{}, "POST", "/api/me/assets/upload-sessions", body, map[string]string{"Idempotency-Key": "kind-no-session-0001"}, 401)
	created := create(writer, "kind-shared-upload-0001")
	if retry := create(reader, "kind-shared-upload-0001"); retry.Asset.ID != created.Asset.ID || retry.UploadSession.ID != created.UploadSession.ID {
		t.Fatal("cross-Pod idempotent creation changed IDs")
	}
	l.request(reader, alice, "POST", "/api/me/assets/upload-sessions", strings.Replace(body, "private.txt", "different.txt", 1), map[string]string{"Idempotency-Key": "kind-shared-upload-0001"}, 409)
	l.request(writer, alice, "POST", "/api/me/assets/upload-sessions", strings.TrimSuffix(body, "}")+`,"owner_id":"`+bob.account+`"}`, map[string]string{"Idempotency-Key": "kind-forged-owner-0001"}, 400)

	cut := platform.AssetUploadChunkBytes
	upload(writer, alice, created, 0, cut, 200)
	var progress struct {
		ReceivedBytes, ExpectedBytes int64
		State                        string
	}
	kindDecode(t, l.request(reader, alice, "GET", "/api/me/upload-sessions/"+created.UploadSession.ID, "", nil, 200), &progress)
	row := l.assetRecord(created.Asset.ID)
	if progress.ReceivedBytes != int64(cut) || progress.ExpectedBytes != int64(len(data)) || progress.State != "active" || row.Received != int64(cut) || row.AssetReceived != row.Received || row.Owner != alice.account || row.State != "staging" {
		t.Fatal("Pod B did not recover Pod A's committed upload progress")
	}
	assertMultipart(row, cut)
	l.request(reader, alice, "GET", "/api/me/assets/"+created.Asset.ID+"/content", "", nil, 409)
	fileDraft := func(id string) string {
		return `{"title":"private kind S3 draft","summary":"","description":"","entries":[{"side":"human","title":"private file","source":"mine","artifacts":[{"kind":"file","assetId":"` + id + `"}],"permissions":{"cloudUse":false}}]}`
	}
	l.request(reader, alice, "POST", "/api/tasks", fileDraft(created.Asset.ID), map[string]string{"Idempotency-Key": "kind-staging-binding-01"}, 409)
	// B cannot write, inspect, abort or complete A's resource; nonexistent and
	// unauthorized metadata must expose the same public Problem code.
	denied := l.request(reader, bob, "GET", "/api/me/assets/"+created.Asset.ID, "", nil, 404)
	missing := l.request(reader, bob, "GET", "/api/me/assets/ast_00000000000000000000000000000000", "", nil, 404)
	var p1, p2 struct{ Code string }
	kindDecode(t, denied, &p1)
	kindDecode(t, missing, &p2)
	if p1.Code != p2.Code || p1.Code == "" {
		t.Fatal("asset existence leaked through Problem code")
	}
	l.request(reader, bob, "GET", "/api/me/assets/"+created.Asset.ID+"/content", "", nil, 404)
	upload(reader, bob, created, cut, len(data), 404)
	complete(reader, bob, created, 404)
	l.request(reader, bob, "DELETE", "/api/me/assets/upload-sessions/"+created.UploadSession.ID, "", nil, 404)
	upload(reader, alice, created, 0, cut, 409) // Explicit offset conflict, no blind replay.
	assertMultipart(row, cut)
	upload(reader, alice, created, cut, len(data), 200)
	var ready kindAsset
	kindDecode(t, complete(reader, alice, created, 200), &ready)
	if ready.ID != created.Asset.ID || ready.State != "ready" || ready.Sha256 != digest || ready.Bytes != int64(len(data)) {
		t.Fatal("invalid completed Asset metadata")
	}
	kindDecode(t, complete(writer, alice, created, 200), &ready)
	if ready.ID != created.Asset.ID || ready.State != "ready" {
		t.Fatal("cross-Pod repeated completion changed the result")
	}
	row = assertReady(created)
	assertMissing(row.Staging)
	assertObject(row.Final, data)
	assertDownload(writer, created)
	assertDownload(reader, created)
	t.Log("PASS: real HTTP -> Gateway -> Identity -> pinned Asset A/B -> PostgreSQL/S3 upload/resume/complete/download, size/hash/state and A/B isolation")

	draftBody := fileDraft(created.Asset.ID)
	var draft struct {
		ID, State, AuthorID string
		Revision            int
		Content             json.RawMessage
	}
	kindDecode(t, l.request(writer, alice, "POST", "/api/tasks", draftBody, map[string]string{"Idempotency-Key": "kind-asset-draft-0001"}, 201), &draft)
	if draft.ID == "" || draft.State != "draft" || draft.AuthorID != alice.account || draft.Revision != 1 || !strings.Contains(string(draft.Content), created.Asset.ID) {
		t.Fatal("private draft binding did not persist correctly")
	}
	draftPath := "/api/me/tasks/" + draft.ID
	kindDecode(t, l.request(reader, alice, "GET", draftPath, "", nil, 200), &draft)
	update := `{"expectedRevision":1,"content":` + draftBody + `}`
	kindDecode(t, l.request(reader, alice, "PUT", draftPath, update, nil, 200), &draft)
	if draft.Revision != 2 || draft.State != "draft" || !strings.Contains(string(draft.Content), created.Asset.ID) {
		t.Fatal("replacement lost the private asset reference")
	}
	l.request(writer, alice, "PUT", draftPath, update, nil, 409)
	l.request(reader, bob, "POST", "/api/tasks", draftBody, map[string]string{"Idempotency-Key": "kind-bob-binding-0001"}, 409)
	l.request(reader, bob, "GET", draftPath, "", nil, 404)
	l.request(reader, kindUser{}, "GET", "/api/tasks/"+draft.ID, "", nil, 404)
	t.Log("PASS: Content -> Asset ownership over real mTLS, private create/read/replace, revision conflict and foreign/public denial")

	expired := create(writer, "kind-expired-upload-0001")
	upload(writer, alice, expired, 0, cut, 200)
	expiredRow := l.assetRecord(expired.Asset.ID)
	assertMultipart(expiredRow, cut)
	aborted := create(reader, "kind-aborted-upload-0001")
	upload(reader, alice, aborted, 0, cut, 200)
	abortedRow := l.assetRecord(aborted.Asset.ID)
	l.request(writer, alice, "DELETE", "/api/me/assets/upload-sessions/"+aborted.UploadSession.ID, "", nil, 204)
	// Accelerate expiry only in the disposable test DB. Also expire the ready
	// session to prove cleanup never treats a completed final as abandoned.
	l.sql(fmt.Sprintf("UPDATE asset.upload_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id IN ('%s','%s');", expired.UploadSession.ID, created.UploadSession.ID))
	upload(reader, alice, expired, cut, len(data), 409)
	complete(reader, alice, expired, 409)
	deadline := time.Now().Add(95 * time.Second)
	for {
		e, ab := l.assetRecord(expired.Asset.ID), l.assetRecord(aborted.Asset.ID)
		if e.Cleaned && ab.Cleaned {
			if e.State != "failed" || e.SessionState != "expired" || ab.State != "failed" || ab.SessionState != "aborted" {
				t.Fatal("cleanup committed wrong terminal states")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("real Asset cleanup ticker did not finish expired/aborted staging")
		}
		time.Sleep(2 * time.Second)
	}
	for _, key := range []string{expiredRow.Staging, expiredRow.Final, abortedRow.Staging, abortedRow.Final} {
		assertMissing(key)
	}
	assertObject(assertReady(created).Final, data)
	assertDownload(reader, created)
	l.request(reader, alice, "POST", "/api/tasks", fileDraft(expired.Asset.ID), map[string]string{"Idempotency-Key": "kind-expired-binding-01"}, 409)
	l.request(reader, alice, "POST", "/api/tasks", fileDraft(aborted.Asset.ID), map[string]string{"Idempotency-Key": "kind-aborted-binding-01"}, 409)
	t.Log("PASS: real cleanup workers remove expired/aborted staging, persist cleanup markers and preserve completed final")

	l.kube("", "delete", "pod", a.Metadata.Name, "--wait=true")
	l.kube("", "rollout", "status", "deployment/asset", "--timeout=120s")
	kindDecode(t, l.kube("", "get", "pods", "-l", "app=asset", "-o", "json"), &pods)
	newIP := ""
	for _, pod := range pods.Items {
		if pod.Metadata.Name != b.Metadata.Name && pod.Metadata.Name != a.Metadata.Name {
			newIP = pod.Status.PodIP
		}
	}
	if newIP == "" {
		t.Fatal("no replacement Asset Pod")
	}
	// Drop the old pinned gateway before adding a replacement to keep lab
	// resource use bounded. Its port-forward is still cleaned by the fixture.
	l.kube("", "delete", "pod", "asset-verify-writer", "--wait=true")
	restarted := l.pinnedGateway("asset-verify-restarted", newIP)
	assertDownload(restarted, created)
	assertReady(created)
	l.request(restarted, alice, "GET", draftPath, "", nil, 200)
	t.Log("PASS: replacement Asset OS process reads the same persisted file and private reference")

	recovering := create(reader, "kind-recover-upload-0001")
	upload(reader, alice, recovering, 0, cut, 200)
	upload(reader, alice, recovering, cut, len(data), 200)
	recoveryRow := l.assetRecord(recovering.Asset.ID)
	// Fault injection: actual S3 copy+delete succeeds before a DB commit.
	// Another real Pod must recover the final, not generate a new asset.
	manifest := make([]types.CompletedPart, len(recoveryRow.Parts))
	for i, p := range recoveryRow.Parts {
		manifest[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	if _, err = remote.CompleteMultipartUpload(t.Context(), &s3.CompleteMultipartUploadInput{Bucket: aws.String(cfg.Bucket), Key: aws.String(recoveryRow.Staging), UploadId: aws.String(recoveryRow.StorageID), MultipartUpload: &types.CompletedMultipartUpload{Parts: manifest}}); err != nil {
		t.Fatal("prepare real multipart-before-DB recovery", err)
	}
	if err = store.Move(t.Context(), recoveryRow.Staging, recoveryRow.Final); err != nil {
		t.Fatal("prepare real copy-before-DB recovery fault")
	}
	assertMissing(recoveryRow.Staging)
	complete(restarted, alice, recovering, 200)
	assertReady(recovering)
	assertDownload(restarted, recovering)
	t.Log("PASS: different Asset Pod recovers copy+delete-before-DB success using the same asset ID")

	// Exercise a real storage outage only in this disposable namespace. Readiness
	// must fail and a failed chunk must not advance the durable offset. Always
	// restore the StatefulSet, including when a later assertion fails.
	pending := create(reader, "kind-storage-outage-001")
	healthPort := l.forward("pod/"+b.Metadata.Name, 8081)
	health := &http.Client{Timeout: 5 * time.Second}
	healthStatus := func() int {
		t.Helper()
		req, e := http.NewRequestWithContext(t.Context(), "GET", "http://127.0.0.1:"+healthPort+"/readyz", nil)
		if e != nil {
			t.Fatal(e)
		}
		response, e := health.Do(req)
		if e != nil {
			t.Fatal("Asset readiness probe transport failed")
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	waitHealth := func(expected int) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for healthStatus() != expected {
			if time.Now().After(deadline) {
				t.Fatalf("Asset readiness did not become %d", expected)
			}
			time.Sleep(time.Second)
		}
	}
	waitHealth(204)
	storeRestored := false
	t.Cleanup(func() {
		if !storeRestored {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			if err := l.command(ctx, "", "scale", "statefulset/asset-store", "--replicas=1").Run(); err != nil {
				t.Error("could not restore disposable S3 store")
			}
		}
	})
	l.kube("", "scale", "statefulset/asset-store", "--replicas=0")
	l.kube("", "wait", "--for=delete", "pod/asset-store-0", "--timeout=90s")
	waitHealth(503)
	// An absent Pod can cause an immediate connection failure or a network
	// timeout. Both must fail closed, preserving the P6.2 error distinction.
	// A fast 503 can close an unread 5 MiB HTTP body with TCP RST. kubectl
	// port-forward exits on that stream error, even though the HTTP rejection
	// was received correctly. Isolate this intentional fault from the reader's
	// persistent tunnel; same Gateway Pod/Asset, no request retry or weaker check.
	faultUpload := l.httpClient(l.forward("pod/asset-verify-reader", 8080))
	upload(faultUpload, alice, pending, 0, cut, 503, 504)
	if row := l.assetRecord(pending.Asset.ID); row.Received != 0 || row.AssetReceived != 0 || row.State != "staging" || row.SessionState != "active" {
		t.Fatal("unavailable storage incorrectly committed chunk progress")
	}
	l.request(reader, alice, "GET", "/api/me/assets/"+created.Asset.ID+"/content", "", nil, 503, 504)
	l.kube("", "scale", "statefulset/asset-store", "--replicas=1")
	l.kube("", "rollout", "status", "statefulset/asset-store", "--timeout=120s")
	waitHealth(204)
	storeRestored = true
	// Recovery is not just one pinned Asset Pod: the next test uses the normal
	// Service, whose headless gRPC clients must resolve replacement endpoints.
	// Keep the actual HTTP/file expectation unchanged; bound convergence here.
	normal := l.httpClientAddress(os.Getenv("HUMAN_WORTH_VERIFY_GATEWAY_ADDRESS"))
	normal.Transport.(*http.Transport).DisableKeepAlives = true
	l.waitGatewayEndpoints()
	assertDownload(normal, created)
	l.request(normal, alice, "GET", draftPath, "", nil, 200)
	t.Log("PASS: both normal Gateway Service endpoints recovered after S3/Asset replacement; real file and Content request succeeded")
	assertDownload(restarted, created)
	upload(reader, alice, pending, 0, cut, 200)
	l.request(reader, alice, "DELETE", "/api/me/assets/upload-sessions/"+pending.UploadSession.ID, "", nil, 204)
	t.Log("PASS: real S3 outage fails readiness/upload/download closed, preserves DB offset, and recovers using the same PVC")

	l.request(restarted, alice, "POST", "/api/auth/logout", "", nil, 204)
	l.request(reader, alice, "GET", "/api/me/assets/"+created.Asset.ID, "", nil, 401)
	l.request(reader, alice, "GET", "/api/me/assets/"+created.Asset.ID+"/content", "", nil, 401)
	l.request(reader, alice, "GET", draftPath, "", nil, 401)
	t.Log("PASS: logout revocation denies new Asset and Content requests across Pods")
}
