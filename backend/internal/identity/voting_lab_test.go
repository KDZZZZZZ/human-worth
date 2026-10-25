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
	"testing"
	"time"
)

// Real disposable Pods and NodePort/Service, not port-forward fault isolation.
func TestKindVotingDeployment(t *testing.T) {
	l := newKindLab(t)
	client := l.httpClientAddress(getenvRequired(t, "HUMAN_WORTH_VERIFY_GATEWAY_ADDRESS"))
	client.Transport.(*http.Transport).DisableKeepAlives = true
	l.client = client
	author, voter, admin := l.user(), l.user(), l.user()
	l.sql(fmt.Sprintf(`UPDATE identity.accounts SET role='admin' WHERE id='%s'`, admin.account))
	var pods struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     struct{ NodeName string }
			Status   struct{ PodIP string }
		}
	}
	kindDecode(t, l.kube("", "get", "pods", "-l", "app=voting", "-o", "json"), &pods)
	if len(pods.Items) != 2 || pods.Items[0].Spec.NodeName == pods.Items[1].Spec.NodeName {
		t.Fatal("two Voting Pods on distinct nodes required")
	}
	a := l.pinnedGatewayTarget("voting-a", "VOTING_TARGET", pods.Items[0].Status.PodIP)
	b := l.pinnedGatewayTarget("voting-b", "VOTING_TARGET", pods.Items[1].Status.PodIP)
	data := []byte("kind voting actual shared S3 candidate\n")
	var upload kindUpload
	body := fmt.Sprintf(`{"filename":"voting.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%x"}`, len(data), sha256.Sum256(data))
	kindDecode(t, l.request(client, author, "POST", "/api/me/assets/upload-sessions", body, map[string]string{"Idempotency-Key": "kind-voting-upload-01"}, 201), &upload)
	l.request(client, author, "PUT", "/api/me/assets/upload-sessions/"+upload.UploadSession.ID+"/content", string(data), map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data))}, 200)
	l.request(client, author, "POST", "/api/me/assets/"+upload.Asset.ID+"/complete", `{"uploadSessionId":"`+upload.UploadSession.ID+`"}`, nil, 200)
	draft := `{"title":"kind voting parent","summary":"","description":"frozen","entries":[{"side":"human","title":"human","source":"self","artifacts":[{"kind":"file","assetId":"` + upload.Asset.ID + `"}],"permissions":{"cloudUse":false}},{"side":"agent","title":"agent","source":"model","artifacts":[{"kind":"text","text":"complete agent work"}],"permissions":{"cloudUse":false},"agentConfiguration":{"model":"PRIVATE_MODEL"}}]}`
	var task struct {
		ID       string
		ReviewID *string
	}
	kindDecode(t, l.request(client, author, "POST", "/api/tasks", draft, map[string]string{"Idempotency-Key": "kind-voting-task-01"}, 201), &task)
	kindDecode(t, l.request(client, author, "POST", "/api/me/tasks/"+task.ID+"/review-requests", `{"expectedRevision":1}`, nil, 200), &task)
	poll := func(review, state string) {
		t.Helper()
		until := time.Now().Add(110 * time.Second)
		for time.Now().Before(until) {
			req, _ := http.NewRequestWithContext(t.Context(), "GET", "https://worth.oopsbox.cn/api/admin/reviews/"+review, nil)
			req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: admin.session})
			r, e := client.Do(req)
			if e == nil {
				raw, _ := io.ReadAll(r.Body)
				r.Body.Close()
				var got kindReview
				if r.StatusCode == 200 && json.Unmarshal(raw, &got) == nil && got.Status == state {
					return
				}
			}
			time.Sleep(300 * time.Millisecond)
		}
		t.Fatal("review did not reach " + state)
	}
	poll(*task.ReviewID, "pending")
	l.request(client, admin, "POST", "/api/admin/reviews/"+*task.ReviewID+"/decision", `{"decision":"approve","reason":"Voting verification"}`, nil, 202)
	poll(*task.ReviewID, "approved")
	base := "/api/tasks/" + task.ID
	// Exercise the new read and administration services through the deployed
	// Gateway, with their actual mTLS and database grants.
	l.request(client, voter, "POST", base+"/comments", `{"body":"Compare the complete works"}`, nil, 201)
	var comments struct{ Items []struct{ Body string } }
	kindDecode(t, l.request(client, kindUser{}, "GET", base+"/comments", "", nil, 200), &comments)
	if len(comments.Items) != 1 || comments.Items[0].Body != "Compare the complete works" {
		t.Fatal("deployed discussion lost its persisted comment")
	}
	var found struct{ Items []struct{ ID string } }
	kindDecode(t, l.request(client, kindUser{}, "GET", "/api/tasks?order=hot&q=kind%20voting", "", nil, 200), &found)
	if len(found.Items) != 1 || found.Items[0].ID != task.ID {
		t.Fatal("deployed Discovery failed to find the published task")
	}
	var board struct {
		Overall struct{ PublishedTasks int64 }
	}
	kindDecode(t, l.request(client, kindUser{}, "GET", "/api/board", "", nil, 200), &board)
	if board.Overall.PublishedTasks < 1 {
		t.Fatal("deployed public board omitted published tasks")
	}
	l.request(client, voter, "GET", "/api/admin/dashboard", "", nil, 403)
	dashboard := l.request(client, admin, "GET", "/api/admin/dashboard", "", nil, 200)
	if !bytes.Contains(dashboard, []byte(`"activeRuns"`)) || bytes.Contains(dashboard, []byte(`"quota"`)) {
		t.Fatal("admin summary did not join Challenge or exposed quota")
	}
	l.request(client, admin, "GET", "/api/admin/audit-events", "", nil, 200)
	var ballot struct {
		ID          string
		Entries     []struct{ ID string }
		CurrentVote *struct {
			ID       string
			Revision int64
		}
	}
	raw := l.request(a, voter, "GET", base+"/ballot", "", nil, 200)
	if bytes.Contains(raw, []byte("PRIVATE_MODEL")) || bytes.Contains(raw, []byte("totalVotes")) {
		t.Fatal("ballot private/statistical leak")
	}
	kindDecode(t, raw, &ballot)
	if len(ballot.Entries) != 2 {
		t.Fatal("complete initial set missing")
	}
	voteBody := fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, ballot.ID, ballot.Entries[0].ID)
	var vote struct {
		ID       string
		Revision int64
	}
	kindDecode(t, l.request(a, voter, "PUT", base+"/vote", voteBody, nil, 200), &vote)
	var replay struct {
		ID       string
		Revision int64
	}
	kindDecode(t, l.request(b, voter, "PUT", base+"/vote", voteBody, nil, 200), &replay)
	if replay.ID != vote.ID || replay.Revision != 1 {
		t.Fatal("cross replica vote receipt mismatch")
	}
	file := base + "/entries/" + ballot.Entries[0].ID + "/assets/" + upload.Asset.ID + "/content"
	if !bytes.Equal(l.request(client, kindUser{}, "GET", file, "", nil, 200), data) {
		t.Fatal("candidate bytes changed")
	}
	l.request(a, voter, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":false}`, nil, 422)
	var stats struct {
		TotalVotes  int64
		EntryVotes  []struct{ Votes int64 }
		Eligibility struct {
			ClosedAt          string
			PermanentlyClosed bool
		}
	}
	kindDecode(t, l.request(a, voter, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200), &stats)
	if stats.TotalVotes != 1 || len(stats.EntryVotes) != 2 || !stats.Eligibility.PermanentlyClosed {
		t.Fatal("stats/closure incomplete")
	}
	l.request(b, voter, "PUT", base+"/vote", voteBody, nil, 403)
	restore := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		if e := l.command(ctx, "", "scale", "deployment/voting", "--replicas=2").Run(); e != nil {
			t.Error(e)
			return
		}
		if e := l.command(ctx, "", "rollout", "status", "deployment/voting", "--timeout=120s").Run(); e != nil {
			t.Error(e)
		}
	}
	t.Cleanup(restore)
	l.kube("", "scale", "deployment/voting", "--replicas=0")
	l.kube("", "wait", "--for=delete", "pod", "-l", "app=voting", "--timeout=90s")
	time.Sleep(15 * time.Second)
	var endpoints struct {
		Items []struct {
			Endpoints []struct{ Conditions struct{ Ready *bool } }
		}
	}
	kindDecode(t, l.kube("", "get", "endpointslices", "-l", "kubernetes.io/service-name=gateway", "-o", "json"), &endpoints)
	ready := 0
	for _, s := range endpoints.Items {
		for _, e := range s.Endpoints {
			if e.Conditions.Ready != nil && *e.Conditions.Ready {
				ready++
			}
		}
	}
	if ready != 2 {
		t.Fatal("Voting outage removed Gateway endpoints", ready)
	}
	l.request(client, voter, "GET", "/api/me", "", nil, 200)
	l.request(client, kindUser{}, "GET", base, "", nil, 200)
	l.request(client, kindUser{}, "GET", base+"/entries", "", nil, 200)
	kindDecode(t, l.request(client, kindUser{}, "GET", "/api/tasks?q=kind%20voting", "", nil, 200), &found)
	if len(found.Items) != 1 || found.Items[0].ID != task.ID {
		t.Fatal("Voting outage removed ordinary discovery")
	}
	l.request(client, kindUser{}, "GET", "/api/board", "", nil, 503, 504)
	l.request(client, voter, "GET", base+"/ballot", "", nil, 503, 504)
	// While Voting is offline an approval becomes a durable pending intent, never
	// a final Moderation receipt or an unguarded newly published directory.
	entryBody := `{"side":"human","title":"outage addition","source":"self","artifacts":[{"kind":"text","text":"complete new work"}],"permissions":{"cloudUse":false}}`
	var entry struct{ ID, ReviewID string }
	kindDecode(t, l.request(client, voter, "POST", base+"/entries", entryBody, map[string]string{"Idempotency-Key": "kind-voting-addition-01"}, 201), &entry)
	poll(entry.ReviewID, "pending")
	l.request(client, admin, "POST", "/api/admin/reviews/"+entry.ReviewID+"/decision", `{"decision":"approve","reason":""}`, nil, 202)
	until := time.Now().Add(20 * time.Second)
	for l.sql(`SELECT count(*) FROM content.voting_catalog_changes WHERE task_id='`+task.ID+`' AND stage='queued'`) != "1" {
		if time.Now().After(until) {
			t.Fatal("missing queued durable fence")
		}
		time.Sleep(250 * time.Millisecond)
	}
	l.request(client, kindUser{}, "GET", base, "", nil, 404)
	l.request(client, kindUser{}, "GET", file, "", nil, 404)
	restore()
	poll(entry.ReviewID, "approved")
	l.request(client, kindUser{}, "GET", base, "", nil, 200)
	l.request(client, voter, "GET", base+"/ballot", "", nil, 403)
	var after struct {
		Eligibility struct{ ClosedAt string }
		TotalVotes  int64
		EntryVotes  []struct{ Votes int64 }
	}
	kindDecode(t, l.request(client, voter, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200), &after)
	if after.Eligibility.ClosedAt != stats.Eligibility.ClosedAt || after.TotalVotes != 1 || len(after.EntryVotes) != 3 {
		t.Fatal("Pod restart/added work restored voting or duplicated counts")
	}
	t.Log("PASS: real dual Voting Pods, S3 file, cross-replica receipt, permanent closure, normal NodePort fault isolation and queued approval recovery")
}
