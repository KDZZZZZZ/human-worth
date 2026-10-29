//go:build lab

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// A long shared dependency outage plus Pod replacement previously allowed
// empty headless discovery to push a caller past the existing 90s checkpoint.
// Keep that checkpoint and exercise only this disposable namespace.
func TestKindDependencyRecovery(t *testing.T) {
	l := newKindLab(t)
	readyCount := func(app string) int {
		var pods struct {
			Items []struct {
				Metadata struct{ DeletionTimestamp *string }
				Status   struct {
					Conditions []struct{ Type, Status string }
				}
			}
		}
		kindDecode(t, l.kube("", "get", "pods", "-l", "app="+app, "-o", "json"), &pods)
		count := 0
		for _, p := range pods.Items {
			if p.Metadata.DeletionTimestamp != nil {
				continue
			}
			for _, c := range p.Status.Conditions {
				if c.Type == "Ready" && c.Status == "True" {
					count++
				}
			}
		}
		return count
	}
	wait := func(limit time.Duration, label string, predicate func() bool) {
		t.Helper()
		until := time.Now().Add(limit)
		for !predicate() {
			if time.Now().After(until) {
				t.Fatal(label)
			}
			time.Sleep(time.Second)
		}
	}
	discoveryCount := func(service string) int {
		var slices struct {
			Items []struct {
				Endpoints []struct{ Addresses []string }
			}
		}
		kindDecode(t, l.kube("", "get", "endpointslices", "-l", "kubernetes.io/service-name="+service, "-o", "json"), &slices)
		n := 0
		for _, s := range slices.Items {
			for _, e := range s.Endpoints {
				n += len(e.Addresses)
			}
		}
		return n
	}
	normal := l.httpClientAddress(os.Getenv("HUMAN_WORTH_VERIFY_GATEWAY_ADDRESS"))
	normal.Transport.(*http.Transport).DisableKeepAlives = true
	user := l.user()
	l.waitGatewayEndpoints()
	// S3-backed upload/complete/download validates normal business traffic even
	// when a discovered but NOT_SERVING replica has a working gRPC transport.
	data := "synthetic dependency recovery file"
	sum := sha256.Sum256([]byte(data))
	body := fmt.Sprintf(`{"filename":"recovery.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%x"}`, len(data), sum)
	var upload kindUpload
	kindDecode(t, l.request(normal, user, "POST", "/api/me/assets/upload-sessions", body, map[string]string{"Idempotency-Key": "kind-health-recovery-01"}, 201), &upload)
	l.request(normal, user, "PUT", "/api/me/assets/upload-sessions/"+upload.UploadSession.ID+"/content", data, map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data))}, 200)
	l.request(normal, user, "POST", "/api/me/assets/"+upload.Asset.ID+"/complete", `{"uploadSessionId":"`+upload.UploadSession.ID+`"}`, nil, 200)
	download := func() {
		t.Helper()
		if got := string(l.request(normal, user, "GET", "/api/me/assets/"+upload.Asset.ID+"/content", "", nil, 200)); got != data {
			t.Fatal("recovery file changed")
		}
	}
	download()
	{ // One unhealthy discovered replica.
		// Bind a standalone Pod to the Deployment owner so its ReplicaSet cannot
		// adopt it. Only its synthetic S3 endpoint changes; mTLS/policies stay real.
		var deployment struct {
			Metadata struct{ UID string }
			Spec     struct{ Template struct{ Spec map[string]any } }
		}
		kindDecode(t, l.kube("", "get", "deployment", "asset", "-o", "json"), &deployment)
		spec := deployment.Spec.Template.Spec
		for _, item := range spec["containers"].([]any)[0].(map[string]any)["env"].([]any) {
			env := item.(map[string]any)
			if env["name"] == "ASSET_S3_ENDPOINT" {
				delete(env, "valueFrom")
				env["value"] = "http://127.0.0.1:1"
			}
		}
		const bad = "asset-health-unavailable"
		pod := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": bad, "namespace": l.namespace, "labels": map[string]string{"app": "asset"}, "ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "asset", "uid": deployment.Metadata.UID, "controller": true}}}, "spec": spec}
		raw, err := json.Marshal(pod)
		if err != nil {
			t.Fatal(err)
		}
		// Register cleanup before touching replicas so failure restores both paths.
		cleanup := func() {
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
			defer cancel()
			if err := l.command(ctx, "", "delete", "pod", bad, "--ignore-not-found", "--wait=true").Run(); err != nil {
				t.Error("could not delete unhealthy probe Pod")
			}
			if err := l.command(ctx, "", "scale", "deployment/asset", "--replicas=2").Run(); err != nil {
				t.Error("could not restore Asset replicas")
			}
			if err := l.command(ctx, "", "rollout", "status", "deployment/asset", "--timeout=120s").Run(); err != nil {
				t.Error("Asset rollout did not recover")
			}
		}
		t.Cleanup(cleanup)
		l.kube("", "scale", "deployment/asset", "--replicas=1")
		l.kube("", "rollout", "status", "deployment/asset", "--timeout=120s")
		l.kube(string(raw), "create", "-f", "-")
		l.kube("", "wait", "--for=jsonpath={.status.phase}=Running", "pod/"+bad, "--timeout=120s")
		health := &http.Client{Timeout: 5 * time.Second}
		port := l.forward("pod/"+bad, 8081)
		wait(60*time.Second, "synthetic S3 failure did not reject readiness", func() bool {
			response, err := health.Get("http://127.0.0.1:" + port + "/readyz")
			if err != nil {
				return false
			}
			response.Body.Close()
			return response.StatusCode == 503
		})
		wait(45*time.Second, "expected one healthy Asset and both discovery addresses", func() bool { return readyCount("asset") == 1 && discoveryCount("asset-discovery") == 2 })
		// Existing channels might still cache the pre-fault addresses. Recreate both
		// consumers so they certainly discover healthy AND unhealthy endpoints.
		for _, app := range []string{"content", "gateway"} {
			l.kube("", "rollout", "restart", "deployment/"+app)
			l.kube("", "rollout", "status", "deployment/"+app, "--timeout=120s")
		}
		l.waitGatewayEndpoints()
		for range 20 {
			download()
		}
		t.Log("PASS: real mTLS callers discover a NOT_SERVING Asset alongside one healthy replica; 20 normal NodePort S3 downloads succeed")
		cleanup()
	}
	wait(45*time.Second, "two Asset replicas did not return", func() bool { return readyCount("asset") == 2 })
	l.waitGatewayEndpoints()
	{ // Long S3 outage and Asset replacement.
		restored := false
		t.Cleanup(func() {
			if !restored {
				ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
				defer cancel()
				if err := l.command(ctx, "", "scale", "statefulset/asset-store", "--replicas=1").Run(); err != nil {
					t.Error("could not restore S3")
				}
			}
		})
		l.kube("", "scale", "statefulset/asset-store", "--replicas=0")
		l.kube("", "wait", "--for=delete", "pod/asset-store-0", "--timeout=90s")
		wait(45*time.Second, "Asset/Content/Gateway failed to leave readiness", func() bool {
			return readyCount("asset") == 0 && readyCount("content") == 0 && readyCount("gateway") == 0
		})
		// Replace an Asset while every business endpoint is unready. The discovery
		// addresses must remain available throughout the long outage.
		var pods struct {
			Items []struct{ Metadata struct{ Name string } }
		}
		kindDecode(t, l.kube("", "get", "pods", "-l", "app=asset", "-o", "json"), &pods)
		if len(pods.Items) != 2 {
			t.Fatal("expected two Asset Pods")
		}
		l.kube("", "delete", "pod", pods.Items[0].Metadata.Name, "--wait=true")
		wait(90*time.Second, "replacement Asset discovery did not converge", func() bool { return discoveryCount("asset-discovery") == 2 })
		started := time.Now()
		for time.Since(started) < 280*time.Second {
			if discoveryCount("asset-discovery") != 2 || discoveryCount("content-discovery") != 2 {
				t.Fatal("dependency readiness removed discovery addresses")
			}
			if readyCount("asset") != 0 || readyCount("content") != 0 || readyCount("gateway") != 0 {
				t.Fatal("dependency outage incorrectly became Ready")
			}
			remaining := 280*time.Second - time.Since(started)
			if remaining <= 0 {
				break
			}
			if remaining > 30*time.Second {
				remaining = 30 * time.Second
			}
			time.Sleep(remaining)
			t.Logf("S3 remains absent; discovery retained; hold elapsed %.1fs", time.Since(started).Seconds())
		}
		l.kube("", "scale", "statefulset/asset-store", "--replicas=1")
		l.kube("", "rollout", "status", "statefulset/asset-store", "--timeout=120s")
		wait(30*time.Second, "Asset did not recover", func() bool { return readyCount("asset") == 2 })
		restored = true
		began := time.Now()
		l.waitGatewayEndpoints() // Original 90-second endpoint assertion, unchanged.
		wait(30*time.Second, "each Content caller did not recover", func() bool { return readyCount("content") == 2 })
		t.Logf("PASS: both normal Gateway endpoints recovered in %.3fs after two Asset Ready (90s checkpoint unchanged)", time.Since(began).Seconds())
		download()
		l.request(normal, user, "GET", "/api/me/submissions?kind=task&state=draft", "", nil, 200)
		t.Log("PASS: normal NodePort file integrity and Content requests after long S3 outage/Pod replacement")
	}
	// Exercise revoked session rejection on the recovered ordinary route.
	l.request(normal, user, "POST", "/api/auth/logout", "", nil, 204)
	l.request(normal, user, "GET", "/api/me/assets/"+upload.Asset.ID+"/content", "", nil, 401)
	if !strings.HasPrefix(upload.Asset.ID, "ast_") {
		t.Fatal("invalid synthetic upload fixture")
	}
}
