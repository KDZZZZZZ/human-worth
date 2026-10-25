//go:build lab

package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
)

// This is an end-to-end deployment check, not a mock: synthetic actors use
// HTTPS, real Gateway/Identity/Content/Moderation Pods, PostgreSQL and workers.
func TestKindAdditionalEntryDeployment(t *testing.T) {
	l := newKindLab(t)
	nodePort := l.httpClientAddress(getenvRequired(t, "HUMAN_WORTH_VERIFY_GATEWAY_ADDRESS"))
	nodePort.Transport.(*http.Transport).DisableKeepAlives = true
	l.client = nodePort
	author, contributor, admin := l.user(), l.user(), l.user()
	l.sql(fmt.Sprintf("UPDATE identity.accounts SET role='admin' WHERE id='%s';", admin.account))

	var pods struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     struct{ NodeName string }
			Status   struct{ PodIP string }
		}
	}
	kindDecode(t, l.kube("", "get", "pods", "-l", "app=content", "-o", "json"), &pods)
	if len(pods.Items) != 2 || pods.Items[0].Status.PodIP == pods.Items[1].Status.PodIP || pods.Items[0].Spec.NodeName == pods.Items[1].Spec.NodeName {
		t.Fatal("two Content replicas on distinct worker nodes required")
	}
	first := l.pinnedGatewayTarget("additional-entry-content-a", "CONTENT_TARGET", pods.Items[0].Status.PodIP)
	second := l.pinnedGatewayTarget("additional-entry-content-b", "CONTENT_TARGET", pods.Items[1].Status.PodIP)

	// Publish a real parent so additional-entry category/visibility are derived
	// from an immutable, formally approved parent snapshot.
	draftBody := `{"title":"additional-entry parent","summary":"","description":"frozen","category":"绘画","entries":[{"side":"human","title":"initial human","source":"self","artifacts":[{"kind":"text","text":"original human work"}],"permissions":{"cloudUse":false}},{"side":"agent","title":"initial agent","source":"model","artifacts":[{"kind":"text","text":"original agent work"}],"permissions":{"cloudUse":false},"agentConfiguration":{"model":"test"}}]}`
	var task struct {
		ID       string
		State    string
		ReviewID *string
	}
	kindDecode(t, l.request(nodePort, author, "POST", "/api/tasks", draftBody, map[string]string{"Idempotency-Key": "kind-additional-parent-01"}, 201), &task)
	kindDecode(t, l.request(nodePort, author, "POST", "/api/me/tasks/"+task.ID+"/review-requests", `{"expectedRevision":1}`, nil, 200), &task)
	if task.ReviewID == nil {
		t.Fatal("parent review not created")
	}
	parentReview := *task.ReviewID
	waitReview := func(id, state string) kindReview {
		t.Helper()
		deadline := time.Now().Add(105 * time.Second)
		for time.Now().Before(deadline) {
			req, err := http.NewRequestWithContext(t.Context(), "GET", "https://worth.oopsbox.cn/api/admin/reviews/"+id, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: admin.session})
			response, err := nodePort.Do(req)
			if err == nil {
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				var review kindReview
				if readErr == nil && response.StatusCode == 200 && json.Unmarshal(body, &review) == nil && review.Status == state {
					return review
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatalf("review did not reach %s", state)
		return kindReview{}
	}
	waitReview(parentReview, "pending")
	l.request(nodePort, admin, "POST", "/api/admin/reviews/"+parentReview+"/decision", `{"decision":"approve","reason":"parent approved"}`, nil, 202)
	if got := waitReview(parentReview, "approved"); got.DeliveryState != "applied" {
		t.Fatal("parent approval lacks Content receipt")
	}
	publicDetail := "/api/tasks/" + task.ID
	publicEntries := publicDetail + "/entries"
	frozenParent := l.sql(`SELECT jsonb_build_array(snapshot,source_revision,published_at)::text FROM content.reviewed_tasks WHERE task_id='` + task.ID + `'`)
	initialDetail := l.request(nodePort, kindUser{}, "GET", publicDetail, "", nil, 200)
	var initial struct {
		Items []json.RawMessage
	}
	kindDecode(t, l.request(nodePort, kindUser{}, "GET", publicEntries, "", nil, 200), &initial)
	if len(initial.Items) != 2 {
		t.Fatal("formally published parent must contain both initial works")
	}

	// Stop Moderation while preserving both ready Gateways. Submission must be
	// accepted durably through the normal Service and replayed by real workers.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		if err := l.command(ctx, "", "scale", "deployment/moderation", "--replicas=2").Run(); err != nil {
			t.Error("restore disposable Moderation replicas", err)
			return
		}
		if err := l.command(ctx, "", "rollout", "status", "deployment/moderation", "--timeout=120s").Run(); err != nil {
			t.Error("restore disposable Moderation readiness", err)
		}
	})
	l.kube("", "scale", "deployment/moderation", "--replicas=0")
	l.kube("", "wait", "--for=delete", "pod", "-l", "app=moderation", "--timeout=90s")
	time.Sleep(15 * time.Second)
	var slices struct {
		Items []struct {
			Endpoints []struct{ Conditions struct{ Ready *bool } }
		}
	}
	kindDecode(t, l.kube("", "get", "endpointslices", "-l", "kubernetes.io/service-name=gateway", "-o", "json"), &slices)
	ready := 0
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
				ready++
			}
		}
	}
	if ready != 2 {
		t.Fatalf("Moderation outage removed ready Gateway replicas: %d", ready)
	}

	// B's upload is real S3 material, not a link or a seeded metadata row. It
	// remains uploadable/readable even while Moderation is completely offline.
	data := []byte("B's additional S3 work, distinct from the parent's initial bundle\n")
	var upload kindUpload
	uploadBody := fmt.Sprintf(`{"filename":"additional.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%x"}`, len(data), sha256.Sum256(data))
	kindDecode(t, l.request(nodePort, contributor, "POST", "/api/me/assets/upload-sessions", uploadBody, map[string]string{"Idempotency-Key": "kind-additional-upload-01"}, 201), &upload)
	l.request(nodePort, contributor, "PUT", "/api/me/assets/upload-sessions/"+upload.UploadSession.ID+"/content", string(data), map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data))}, 200)
	l.request(nodePort, contributor, "POST", "/api/me/assets/"+upload.Asset.ID+"/complete", `{"uploadSessionId":"`+upload.UploadSession.ID+`"}`, nil, 200)
	if actual := l.request(nodePort, contributor, "GET", "/api/me/assets/"+upload.Asset.ID+"/content", "", nil, 200); !bytes.Equal(actual, data) {
		t.Fatal("B's completed S3 upload changed bytes")
	}
	l.request(nodePort, author, "GET", "/api/me/assets/"+upload.Asset.ID+"/content", "", nil, 404)
	entryBody := `{"side":"agent","title":"additional contributor work","description":"private details","source":"model","artifacts":[{"kind":"file","assetId":"` + upload.Asset.ID + `"}],"permissions":{"cloudUse":false,"statement":"PRIVATE_PERMISSION"},"agentConfiguration":{"model":"PRIVATE_MODEL","prompt":"PRIVATE_PROMPT"}}`
	var entry struct {
		ID, TaskID, AuthorID, Origin, State, ReviewID string
		Revision                                      int64
	}
	created := l.request(nodePort, contributor, "POST", publicEntries, entryBody, map[string]string{"Idempotency-Key": "kind-additional-entry-01"}, 201)
	kindDecode(t, created, &entry)
	if entry.ID == "" || entry.TaskID != task.ID || entry.AuthorID != contributor.account || entry.Origin != "additional" || entry.State != "pending_review" || entry.Revision != 1 || entry.ReviewID == "" {
		t.Fatal("invalid durable additional entry response")
	}
	// Route the owner read through the other real Content replica and retry the
	// same key through it; both must observe one stable submission identity.
	var read struct{ ID, AuthorID, State string }
	kindDecode(t, l.request(second, contributor, "GET", "/api/me/entries/"+entry.ID, "", nil, 200), &read)
	if read.ID != entry.ID || read.AuthorID != contributor.account || read.State != "pending_review" {
		t.Fatal("second Content replica did not read the durable submission")
	}
	var retry struct{ ID string }
	for _, client := range []*http.Client{first, second} {
		kindDecode(t, l.request(client, contributor, "POST", publicEntries, entryBody, map[string]string{"Idempotency-Key": "kind-additional-entry-01"}, 201), &retry)
		if retry.ID != entry.ID {
			t.Fatal("idempotent retry on pinned Content replica changed entry ID")
		}
	}
	l.request(nodePort, author, "GET", "/api/me/entries/"+entry.ID, "", nil, 404)
	l.request(nodePort, admin, "GET", "/api/me/entries/"+entry.ID, "", nil, 404)
	publicFile := publicEntries + "/" + entry.ID + "/assets/" + upload.Asset.ID + "/content"
	l.request(nodePort, kindUser{}, "GET", publicFile, "", nil, 404)
	var pendingPublic struct{ Items []json.RawMessage }
	kindDecode(t, l.request(nodePort, kindUser{}, "GET", publicEntries, "", nil, 200), &pendingPublic)
	if len(pendingPublic.Items) != 2 {
		t.Fatal("pending additional work escaped into public catalog")
	}
	l.request(nodePort, admin, "GET", "/api/admin/reviews/"+entry.ReviewID, "", nil, 503, 504)
	if l.sql("SELECT count(*) FROM content.entry_submissions WHERE id='"+entry.ID+"' AND state='pending_review'") != "1" || l.sql("SELECT count(*) FROM content.entry_review_outbox WHERE id='"+entry.ReviewID+"' AND delivered_at IS NULL") != "1" {
		t.Fatal("Moderation outage lost the durable submission/outbox")
	}

	// Restore Moderation and wait for its independent real delivery runner.
	if err := l.command(t.Context(), "", "scale", "deployment/moderation", "--replicas=2").Run(); err != nil {
		t.Fatal("restore Moderation replicas")
	}
	l.kube("", "rollout", "status", "deployment/moderation", "--timeout=120s")
	l.waitModerationRecovery(admin)
	if review := waitReview(entry.ReviewID, "pending"); review.DeliveryState != "undecided" {
		t.Fatal("entry case arrived already decided")
	}
	firstReviewID := entry.ReviewID
	reviewFile := func(review string) string {
		return "/api/admin/reviews/" + review + "/assets/" + upload.Asset.ID + "/content"
	}
	if actual := l.request(nodePort, admin, "GET", reviewFile(firstReviewID), "", nil, 200); !bytes.Equal(actual, data) {
		t.Fatal("administrator could not read exact pending S3 bytes")
	}
	l.request(nodePort, contributor, "GET", reviewFile(firstReviewID), "", nil, 403)
	l.request(nodePort, author, "GET", reviewFile(firstReviewID), "", nil, 403)
	l.request(nodePort, kindUser{}, "GET", reviewFile(firstReviewID), "", nil, 401)
	l.request(nodePort, admin, "POST", "/api/admin/reviews/"+firstReviewID+"/decision", `{"decision":"reject","reason":"clarify B provenance"}`, nil, 202)
	if review := waitReview(firstReviewID, "rejected"); review.DeliveryState != "applied" || review.Reason == nil || *review.Reason != "clarify B provenance" {
		t.Fatal("rejected revision lacks reason or applied receipt")
	}
	l.request(nodePort, admin, "GET", reviewFile(firstReviewID), "", nil, 404)
	var rejected struct {
		State, ReviewID string
		Revision        int64
		RejectionReason *string
	}
	kindDecode(t, l.request(second, contributor, "GET", "/api/me/entries/"+entry.ID, "", nil, 200), &rejected)
	if rejected.State != "rejected" || rejected.Revision != 1 || rejected.ReviewID != firstReviewID || rejected.RejectionReason == nil || *rejected.RejectionReason != "clarify B provenance" {
		t.Fatal("B did not receive rejected revision/reason through the other Content replica")
	}
	revisedBody := strings.Replace(entryBody, "private details", "corrected B provenance", 1)
	resubmitBody := `{"expectedRevision":1,"content":` + revisedBody + `}`
	stableEntryID := entry.ID
	kindDecode(t, l.request(first, contributor, "PUT", "/api/me/entries/"+entry.ID, resubmitBody, nil, 200), &entry)
	if entry.ID != stableEntryID || entry.Revision != 2 || entry.ReviewID == firstReviewID || entry.ReviewID == "" || entry.State != "pending_review" || entry.AuthorID != contributor.account {
		t.Fatal("rejected resubmission changed B identity or overwrote review history")
	}
	var resubmitRetry struct {
		ID, ReviewID string
		Revision     int64
	}
	kindDecode(t, l.request(second, contributor, "PUT", "/api/me/entries/"+entry.ID, resubmitBody, nil, 200), &resubmitRetry)
	if resubmitRetry.ID != entry.ID || resubmitRetry.ReviewID != entry.ReviewID || resubmitRetry.Revision != 2 {
		t.Fatal("same expectedRevision retry created another submitted version")
	}
	var createRetry struct {
		ID, State, ReviewID string
		Revision            int64
	}
	kindDecode(t, l.request(nodePort, contributor, "POST", publicEntries, entryBody, map[string]string{"Idempotency-Key": "kind-additional-entry-01"}, 201), &createRetry)
	if createRetry.ID != entry.ID || createRetry.Revision != 1 || createRetry.ReviewID != firstReviewID || createRetry.State != "rejected" {
		t.Fatal("create key retry did not recover original revision-1 review result")
	}
	waitReview(entry.ReviewID, "pending")
	if actual := l.request(nodePort, admin, "GET", reviewFile(entry.ReviewID), "", nil, 200); !bytes.Equal(actual, data) {
		t.Fatal("resubmitted pending snapshot lost B's file reference")
	}
	l.request(nodePort, admin, "POST", "/api/admin/reviews/"+entry.ReviewID+"/decision", `{"decision":"approve","reason":"additional approved"}`, nil, 202)
	if review := waitReview(entry.ReviewID, "approved"); review.DeliveryState != "applied" || len(review.EntryIDs) != 1 || review.EntryIDs[0] != entry.ID || review.Submission.Revision != 2 {
		t.Fatal("single-entry approval lacks revision-2 Content receipt/identity")
	}
	l.request(nodePort, admin, "POST", "/api/admin/reviews/"+entry.ReviewID+"/decision", `{"decision":"approve","reason":"additional approved"}`, nil, 200)
	l.request(nodePort, admin, "GET", reviewFile(entry.ReviewID), "", nil, 404)
	var own struct{ State, Origin string }
	kindDecode(t, l.request(nodePort, contributor, "GET", "/api/me/entries/"+entry.ID, "", nil, 200), &own)
	if own.State != "approved" || own.Origin != "additional" {
		t.Fatal("owner submission did not follow formal Moderation approval")
	}
	var list struct {
		Items []struct{ ID, AuthorID, State, Origin string }
	}
	kindDecode(t, l.request(nodePort, contributor, "GET", "/api/me/submissions?kind=entry", "", nil, 200), &list)
	found := false
	for _, item := range list.Items {
		if item.ID == entry.ID && item.AuthorID == contributor.account && item.State == "approved" && item.Origin == "additional" {
			found = true
		}
	}
	if !found {
		t.Fatal("default own entry listing omitted approved additional work")
	}
	var public struct {
		Items []struct {
			ID, Title string
			Author    struct{ ID string }
			Artifacts []struct{ Kind, AssetID string }
		}
	}
	for _, client := range []*http.Client{nodePort, first, second} {
		raw := l.request(client, kindUser{}, "GET", publicEntries, "", nil, 200)
		kindDecode(t, raw, &public)
		if len(public.Items) != 3 || public.Items[2].ID != entry.ID || public.Items[2].Author.ID != contributor.account || public.Items[2].Title != "additional contributor work" || len(public.Items[2].Artifacts) != 1 || public.Items[2].Artifacts[0].Kind != "file" || public.Items[2].Artifacts[0].AssetID != upload.Asset.ID {
			t.Fatal("public catalog must contain two initial works plus B's approved file work")
		}
		for _, denied := range []string{"PRIVATE_", "permissions", "agentConfiguration", "clarify B provenance", "additional approved", "reviewerId", "deliveryState"} {
			if bytes.Contains(raw, []byte(denied)) {
				t.Fatal("public additional-entry projection leaked private review/configuration data")
			}
		}
		var mixed struct{ Items []json.RawMessage }
		kindDecode(t, raw, &mixed)
		for i := range initial.Items {
			if !bytes.Equal(initial.Items[i], mixed.Items[i]) {
				t.Fatal("additional approval changed an initial frozen public work")
			}
		}
		if actual := l.request(client, kindUser{}, "GET", publicFile, "", nil, 200); !bytes.Equal(actual, data) {
			t.Fatal("approved B-author file returned different S3 bytes")
		}
		if !bytes.Equal(initialDetail, l.request(client, kindUser{}, "GET", publicDetail, "", nil, 200)) {
			t.Fatal("additional approval rewrote frozen parent public detail")
		}
	}
	if l.sql(`SELECT jsonb_build_array(snapshot,source_revision,published_at)::text FROM content.reviewed_tasks WHERE task_id='`+task.ID+`'`) != frozenParent {
		t.Fatal("additional approval mutated the frozen parent snapshot/source revision/publication time")
	}
	if l.sql("SELECT count(*) FROM moderation.reviews WHERE object_kind='entry' AND object_id='"+entry.ID+"'") != "2" || l.sql("SELECT count(*) FROM content.entry_decision_receipts WHERE review_id IN ('"+firstReviewID+"','"+entry.ReviewID+"')") != "2" {
		t.Fatal("reject/resubmit/approve duplicated or lost versioned cases/receipts")
	}
	var beforeTightening struct{ NextCursor *string }
	kindDecode(t, l.request(nodePort, kindUser{}, "GET", publicEntries+"?limit=2", "", nil, 200), &beforeTightening)
	if beforeTightening.NextCursor == nil {
		t.Fatal("missing initial-to-additional page cursor")
	}
	nextPage := publicEntries + "?limit=2&cursor=" + url.QueryEscape(*beforeTightening.NextCursor)
	entryReceipt := l.sql(`SELECT response::text FROM content.entry_decision_receipts WHERE review_id='` + entry.ReviewID + `'`)
	parentReceipt := l.sql(`SELECT response::text FROM content.review_decision_receipts WHERE review_id='` + parentReview + `'`)

	// Approval above uses ONLY real Moderation Pods and workers. Reuse the
	// existing disposable-CA Moderation mTLS helper solely for trusted state
	// tightening, never to seed publication or bypass the formal review flow.
	reviewer := l.reviewClient()
	applyTightening := func(request *contentpb.ApplyAssetReviewRequest) int64 {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		response, err := reviewer.ApplyAssetReview(ctx, request)
		if err != nil {
			t.Fatal("trusted Moderation visibility tightening failed", err)
		}
		return response.Revision
	}
	if revision := applyTightening(&contentpb.ApplyAssetReviewRequest{
		DecisionId: "kind-additional-revoke-01", TaskId: task.ID, SubmissionRevision: 1, ExpectedRevision: 1,
		Decision: &contentpb.ApplyAssetReviewRequest_EntryReview{EntryReview: &contentpb.EntryAssetReview{EntryId: entry.ID, State: contentpb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVAL_REVOKED}},
	}); revision != 2 {
		t.Fatal("additional revocation did not advance parent visibility revision")
	}
	checkRevoked := func() {
		t.Helper()
		for _, client := range []*http.Client{nodePort, first, second} {
			l.request(client, kindUser{}, "GET", publicFile, "", nil, 404)
			var page struct {
				Items      []struct{ ID string }
				NextCursor *string
			}
			kindDecode(t, l.request(client, kindUser{}, "GET", publicEntries, "", nil, 200), &page)
			if len(page.Items) != 2 || page.Items[0].ID == entry.ID || page.Items[1].ID == entry.ID {
				t.Fatal("revoked additional entry remained publicly listed")
			}
			kindDecode(t, l.request(client, kindUser{}, "GET", nextPage, "", nil, 200), &page)
			if len(page.Items) != 0 || page.NextCursor != nil {
				t.Fatal("old cursor did not recheck current additional-entry visibility")
			}
			l.request(client, kindUser{}, "GET", publicDetail, "", nil, 200)
		}
		kindDecode(t, l.request(nodePort, contributor, "GET", "/api/me/entries/"+entry.ID, "", nil, 200), &own)
		if own.State != "approval_revoked" {
			t.Fatal("B's own state did not follow trusted entry revocation")
		}
	}
	checkRevoked()
	replay := func(reviewID string) {
		t.Helper()
		// At-least-once fault injection only. The real Moderation runner replays
		// the stored decision ID/hash; no new decision or approval is invented.
		l.sql(`UPDATE moderation.decision_outbox SET delivered_at=NULL,available_at=clock_timestamp()-interval '1 second' WHERE id='` + reviewID + `';`)
		deadline := time.Now().Add(105 * time.Second)
		for l.sql(`SELECT count(*) FROM moderation.decision_outbox WHERE id='`+reviewID+`' AND delivered_at IS NOT NULL`) != "1" {
			if time.Now().After(deadline) {
				t.Fatal("real worker did not acknowledge old approval replay")
			}
			time.Sleep(300 * time.Millisecond)
		}
		if review := waitReview(reviewID, "approved"); review.DeliveryState != "applied" {
			t.Fatal("old approval lost its applied receipt on replay")
		}
		if l.sql(`SELECT response::text FROM content.entry_decision_receipts WHERE review_id='`+entry.ReviewID+`'`) != entryReceipt || l.sql(`SELECT response::text FROM content.review_decision_receipts WHERE review_id='`+parentReview+`'`) != parentReceipt {
			t.Fatal("old approval replay rewrote immutable Content receipts")
		}
	}
	replay(entry.ReviewID)
	checkRevoked()
	if revision := applyTightening(&contentpb.ApplyAssetReviewRequest{
		DecisionId: "kind-additional-takedown-01", TaskId: task.ID, SubmissionRevision: 1, ExpectedRevision: 2,
		Decision: &contentpb.ApplyAssetReviewRequest_TaskState{TaskState: contentpb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN},
	}); revision != 3 {
		t.Fatal("parent takedown did not advance current visibility revision")
	}
	checkDown := func() {
		t.Helper()
		for _, client := range []*http.Client{nodePort, first, second} {
			for _, path := range []string{publicDetail, publicEntries, nextPage, publicFile} {
				l.request(client, kindUser{}, "GET", path, "", nil, 404)
			}
		}
	}
	checkDown()
	replay(parentReview)
	replay(entry.ReviewID)
	checkDown()
	if l.sql(`SELECT count(*) FROM content.reviewed_tasks WHERE task_id='`+task.ID+`' AND state='taken_down' AND revision=3 AND next_entry_position=33`) != "1" || l.sql(`SELECT count(*) FROM content.reviewed_entries WHERE id='`+entry.ID+`' AND state='approval_revoked' AND position=32`) != "1" {
		t.Fatal("old receipts revived parent/work or allocated a second catalog position")
	}

	// A real Content Pod restart must retain owner/public state and the normal
	// Service must recover without falling back to stale approval authority.
	podNames := strings.Fields(string(l.kube("", "get", "pods", "-l", "app=content", "-o", "jsonpath={.items[*].metadata.name}")))
	if len(podNames) != 2 {
		t.Fatal("expected both Content replicas before restart check")
	}
	l.kube("", "delete", "pod", podNames[0], "--wait=true")
	l.kube("", "rollout", "status", "deployment/content", "--timeout=120s")
	// Gateway/Asset gRPC clients resolve independently. Allow bounded DNS/lease
	// recovery while preserving each deployed RPC's original deadline.
	deadline := time.Now().Add(105 * time.Second)
	for {
		req, err := http.NewRequestWithContext(t.Context(), "GET", "https://worth.oopsbox.cn/api/me/entries/"+entry.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: contributor.session})
		response, err := nodePort.Do(req)
		if err != nil {
			t.Fatal("normal Service owner read failed after Content restart", err)
		}
		raw, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil {
			t.Fatal("read recovery response", err)
		}
		if response.StatusCode == 200 {
			kindDecode(t, raw, &own)
			if own.State != "approval_revoked" {
				t.Fatal("Content restart revived B's revoked work")
			}
			break
		}
		l.dependencyFailure(response.StatusCode, raw)
		if time.Now().After(deadline) {
			t.Fatal("normal Service owner read did not recover after Content Pod restart")
		}
		time.Sleep(300 * time.Millisecond)
	}
	for _, path := range []string{publicDetail, publicEntries, nextPage, publicFile} {
		l.request(nodePort, kindUser{}, "GET", path, "", nil, 404)
	}
	if actual := l.request(nodePort, contributor, "GET", "/api/me/assets/"+upload.Asset.ID+"/content", "", nil, 200); !bytes.Equal(actual, data) {
		t.Fatal("visibility tightening or restart destroyed B's private S3 upload")
	}
	t.Log("PASS: two Content replicas; normal NodePort during Moderation outage; B's real S3 upload/pending admin read/reject/resubmit/formal approval; trusted entry/parent tightening, old receipt replay cannot revive, and Content Pod recovery")
}

func getenvRequired(t *testing.T, key string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		t.Fatalf("%s is required for normal Service validation", key)
	}
	return value
}
