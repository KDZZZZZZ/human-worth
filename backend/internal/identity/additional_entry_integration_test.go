//go:build integration

package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/repo/postgres"
	identitygrpc "github.com/KDZZZZZZ/human-worth/backend/internal/identity/transport/grpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/moderation"
	"github.com/KDZZZZZZ/human-worth/backend/internal/voting"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type additionalEntryResult struct {
	Kind, ID, TaskID, AuthorID, Origin, State, ReviewID string
	Revision                                            int64
	Content                                             json.RawMessage
	RejectionReason, ApprovedAt                         *string
}

type additionalActor struct {
	cookie        *http.Cookie
	account, csrf string
}

type additionalEntryLab struct {
	votingDB            *pgxpool.Pool
	votes               [2]*voting.Server
	voteClients         [2]votingpb.VotingServiceClient
	catalogs            [2]contentpb.ContentServiceClient
	identity            *identityLab
	contentDB, reviewDB *pgxpool.Pool
	contents            [2]*content.Server
	moderations         [2]*moderation.Server
	senders             [2]moderationpb.ModerationServiceClient
	reviewers           [2]contentpb.ContentServiceClient
	gateways            [2]contentpb.ContentServiceClient
	alice, bob, admin   additionalActor
}

// Independent service instances share real PostgreSQL and S3, not application
// memory. All user commands and initial publication use HTTPS and real mTLS.
func newAdditionalEntryLab(t *testing.T, enableVoting ...bool) *additionalEntryLab {
	t.Helper()
	cfg := sharedS3TestConfig(t)
	l := newIdentityLab(t)
	adb := serviceTestDatabase(t, l, "asset", asset.Migrate, "../../../ops/asset-grants.sql")
	f := &additionalEntryLab{identity: l}
	f.contentDB = serviceTestDatabase(t, l, "content", content.Migrate, "../../../ops/content/grants.sql")
	f.reviewDB = serviceTestDatabase(t, l, "moderation", moderation.Migrate, "../../../ops/moderation/grants.sql")
	for i := range f.contents {
		f.contents[i] = &content.Server{DB: f.contentDB, Identity: l.client(t, i, "content", l.pki)}
	}
	cc := func(i int, caller string) contentpb.ContentServiceClient {
		return contentpb.NewContentServiceClient(serviceTestGRPC(t, l, "content", caller, content.Authorization, func(s *grpc.Server) {
			contentpb.RegisterContentServiceServer(s, f.contents[i])
		}))
	}
	if len(enableVoting) > 0 && enableVoting[0] {
		f.votingDB = serviceTestDatabase(t, l, "voting", voting.Migrate, "../../../ops/voting/grants.sql")
	}
	for i := range f.contents {
		var vc votingpb.VotingServiceClient
		if f.votingDB != nil {
			f.catalogs[i] = cc(i, "voting")
			f.votes[i] = &voting.Server{DB: f.votingDB, Identity: l.client(t, i, "voting", l.pki), Content: f.catalogs[i]}
			rpc := func(caller string) votingpb.VotingServiceClient {
				return votingpb.NewVotingServiceClient(serviceTestGRPC(t, l, "voting", caller, voting.Authorization, func(s *grpc.Server) { votingpb.RegisterVotingServiceServer(s, f.votes[i]) }))
			}
			f.contents[i].Voting = rpc("content")
			vc = rpc("gateway")
			f.voteClients[i] = vc
		}
		store, err := asset.NewS3Storage(cfg)
		must(t, err)
		as := layeredAssetServer(adb, store, l.client(t, i, "asset", l.pki), cc(i, "asset"))
		ac := func(caller string) assetpb.AssetServiceClient {
			return assetpb.NewAssetServiceClient(serviceTestGRPC(t, l, "asset", caller, asset.Authorization, func(s *grpc.Server) {
				assetpb.RegisterAssetServiceServer(s, as)
			}))
		}
		f.contents[i].Asset = ac("content")
		f.reviewers[i], f.gateways[i] = cc(i, "moderation"), cc(i, "gateway")
		f.moderations[i] = &moderation.Server{DB: f.reviewDB, Identity: l.client(t, i, "moderation", l.pki), Content: f.reviewers[i]}
		mc := func(caller string) moderationpb.ModerationServiceClient {
			return moderationpb.NewModerationServiceClient(serviceTestGRPC(t, l, "moderation", caller, moderation.Authorization, func(s *grpc.Server) {
				moderationpb.RegisterModerationServiceServer(s, f.moderations[i])
			}))
		}
		f.senders[i] = mc("content")
		h, err := gateway.New(l.clients[i], gateway.Options{Voting: vc, Origin: l.origin, Content: f.gateways[i], Asset: ac("gateway"), Moderation: mc("gateway"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		must(t, err)
		l.web[i].Config.Handler = h
	}
	login := func(subject string) additionalActor {
		c, me := l.login(t, subject, nil)
		return additionalActor{c, me.Account.Id, me.CsrfToken}
	}
	f.alice, f.bob = login("additional-alice"), login("additional-bob")
	firstAdmin := login("additional-admin")
	must(t, identitygrpc.Error(application.NewAdmin(postgres.New(l.db), randomToken).ChangeAccount(t.Context(), "test-operator", dto.ChangeAccountInput{AccountID: firstAdmin.account, Role: "admin", State: "active", ExpectedVersion: 1})))
	f.admin = login("additional-admin")
	return f
}

func (f *additionalEntryLab) request(t *testing.T, replica int, actor additionalActor, method, path, body string, extra map[string]string, want int) *http.Response {
	t.Helper()
	headers := map[string]string{"Origin": f.identity.origin, "X-CSRF-Token": actor.csrf, "Content-Type": "application/json"}
	for k, v := range extra {
		headers[k] = v
	}
	var cookies []*http.Cookie
	if actor.cookie != nil {
		cookies = []*http.Cookie{actor.cookie}
	}
	r := f.identity.request(t, replica, method, path, cookies, body, headers)
	httpStatus(t, r, want)
	return r
}

func additionalInput(side, title, assetID string) string {
	artifact := map[string]string{"kind": "text", "text": "complete submitted work"}
	if assetID != "" {
		artifact = map[string]string{"kind": "file", "assetId": assetID}
	}
	entry := map[string]any{"side": side, "title": title, "description": "frozen work", "source": "self", "artifacts": []any{artifact}, "permissions": map[string]any{"cloudUse": false, "statement": "PRIVATE_PERMISSION"}}
	if side == "agent" {
		entry["agentConfiguration"] = map[string]any{"model": "PRIVATE_MODEL", "prompt": "PRIVATE_PROMPT", "parameters": map[string]any{"private": "PRIVATE_PARAMETER"}}
	}
	raw, _ := json.Marshal(entry)
	return string(raw)
}

func (f *additionalEntryLab) publish(t *testing.T, key string) draftResult {
	t.Helper()
	body := `{"title":"Frozen parent","summary":"summary","description":"Frozen body","category":"绘画","entries":[` + additionalInput("human", "initial human", "") + `,` + additionalInput("agent", "initial agent", "") + `]}`
	var task draftResult
	readJSON(t, f.request(t, 0, f.alice, "POST", "/api/tasks", body, map[string]string{"Idempotency-Key": key}, 201), &task)
	readJSON(t, f.request(t, 1, f.alice, "POST", "/api/me/tasks/"+task.ID+"/review-requests", `{"expectedRevision":1}`, nil, 200), &task)
	_, err := f.contents[0].DeliverReview(t.Context(), f.senders[1])
	must(t, err)
	f.decide(t, *task.ReviewID, "approve", "initial approval")
	return task
}

func (f *additionalEntryLab) submit(t *testing.T, replica int, actor additionalActor, task, key, body string) additionalEntryResult {
	t.Helper()
	var out additionalEntryResult
	readJSON(t, f.request(t, replica, actor, "POST", "/api/tasks/"+task+"/entries", body, map[string]string{"Idempotency-Key": key}, 201), &out)
	if out.Kind != "entry" || out.Origin != "additional" || out.AuthorID != actor.account || out.TaskID != task || out.ReviewID == "" {
		t.Fatal("additional submission lost authoritative identity/origin")
	}
	return out
}

func (f *additionalEntryLab) deliver(t *testing.T) {
	t.Helper()
	had, err := f.contents[0].DeliverEntryReview(t.Context(), f.senders[1])
	must(t, err)
	if !had {
		t.Fatal("missing committed entry outbox item")
	}
}

func (f *additionalEntryLab) decide(t *testing.T, review, decision, reason string) {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"decision": decision, "reason": reason})
	must(t, err)
	f.request(t, 0, f.admin, "POST", "/api/admin/reviews/"+review+"/decision", string(raw), nil, 202).Body.Close()
	had, err := f.moderations[1].DeliverDecision(t.Context())
	must(t, err)
	if !had {
		t.Fatal("missing committed decision outbox item")
	}
	f.request(t, 1, f.admin, "POST", "/api/admin/reviews/"+review+"/decision", string(raw), nil, 200).Body.Close()
}

func (f *additionalEntryLab) upload(t *testing.T, actor additionalActor, key string, data []byte, complete bool) string {
	t.Helper()
	body := fmt.Sprintf(`{"filename":"additional.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%x"}`, len(data), sha256.Sum256(data))
	var out struct{ Asset, UploadSession struct{ ID string } }
	readJSON(t, f.request(t, 0, actor, "POST", "/api/me/assets/upload-sessions", body, map[string]string{"Idempotency-Key": key}, 201), &out)
	if complete {
		f.request(t, 1, actor, "PUT", "/api/me/assets/upload-sessions/"+out.UploadSession.ID+"/content", string(data), map[string]string{"Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data))}, 200).Body.Close()
		f.request(t, 0, actor, "POST", "/api/me/assets/"+out.Asset.ID+"/complete", `{"uploadSessionId":"`+out.UploadSession.ID+`"}`, nil, 200).Body.Close()
	}
	return out.Asset.ID
}

func (f *additionalEntryLab) publicPage(t *testing.T, path string) publicBrowsePage {
	t.Helper()
	r := f.request(t, 1, additionalActor{}, "GET", path, "", nil, 200)
	raw, err := io.ReadAll(r.Body)
	r.Body.Close()
	must(t, err)
	for _, denied := range []string{"PRIVATE_", "agentConfiguration", "permissions", "rejectionReason", "reviewerId", "deliveryState", "snapshot"} {
		if bytes.Contains(raw, []byte(denied)) {
			t.Fatal("public entry projection leaked", denied)
		}
	}
	var page publicBrowsePage
	must(t, json.Unmarshal(raw, &page))
	return page
}

func TestAdditionalEntryHTTPWorkflow(t *testing.T) {
	f := newAdditionalEntryLab(t)
	task := f.publish(t, "additional-parent-key-01")
	list := "/api/tasks/" + task.ID + "/entries"
	initial := f.publicPage(t, list)
	parent := func() string {
		var raw string
		must(t, f.identity.db.QueryRow(t.Context(), `SELECT jsonb_build_array(snapshot,source_revision,published_at)::text FROM content.reviewed_tasks WHERE task_id=$1`, task.ID).Scan(&raw))
		return raw
	}
	frozenParent := parent()
	data := []byte("additional work belongs to Bob, not the parent author\n")
	file := f.upload(t, f.bob, "additional-bob-file-01", data, true)
	otherFile := f.upload(t, f.alice, "additional-alice-file", data, true)
	staging := f.upload(t, f.bob, "additional-staging-01", data, false)
	body := additionalInput("agent", "Bob's frozen work", file)
	createPath := "/api/tasks/" + task.ID + "/entries"
	key := map[string]string{"Idempotency-Key": "additional-create-key-01"}
	t.Run("web_identity_csrf_and_file_ownership", func(t *testing.T) {
		f.request(t, 0, additionalActor{}, "POST", createPath, body, key, 401).Body.Close()
		f.request(t, 0, f.bob, "POST", createPath, body, map[string]string{"Idempotency-Key": key["Idempotency-Key"], "X-CSRF-Token": ""}, 403).Body.Close()
		for _, assetID := range []string{otherFile, staging} {
			f.request(t, 0, f.bob, "POST", createPath, additionalInput("human", "invalid file", assetID), key, 409).Body.Close()
		}
		for _, field := range []string{`"authorId":"forged"`, `"origin":"initial"`, `"state":"approved"`, `"actorAssertion":"forged"`} {
			f.request(t, 0, f.bob, "POST", createPath, strings.TrimSuffix(body, "}")+","+field+"}", key, 400).Body.Close()
		}
		for _, invalid := range []string{strings.Replace(body, `"side":"agent"`, `"side":"human"`, 1), strings.Replace(body, `"side":"agent"`, `"side":"robot"`, 1), `{"content":` + body + `}`} {
			f.request(t, 0, f.bob, "POST", createPath, invalid, key, 400).Body.Close()
		}
	})
	entry := f.submit(t, 0, f.bob, task.ID, key["Idempotency-Key"], body)
	if entry.State != "pending_review" || entry.Revision != 1 || entry.ApprovedAt != nil {
		t.Fatal("entry published without moderation")
	}
	entryPath := "/api/me/entries/" + entry.ID
	publicFile := list + "/" + entry.ID + "/assets/" + file + "/content"
	t.Run("private_author_and_admin_boundaries", func(t *testing.T) {
		for _, actor := range []additionalActor{f.alice, f.admin} {
			f.request(t, 1, actor, "GET", entryPath, "", nil, 404).Body.Close()
			f.request(t, 1, actor, "PUT", entryPath, `{"expectedRevision":1,"content":`+body+`}`, nil, 404).Body.Close()
		}
		f.request(t, 1, f.bob, "GET", entryPath, "", nil, 200).Body.Close()
		f.request(t, 1, additionalActor{}, "GET", publicFile, "", nil, 404).Body.Close()
		if page := f.publicPage(t, list); len(page.Items) != 2 {
			t.Fatal("pending work leaked into public entries")
		}
		r := f.request(t, 0, f.bob, "POST", "/api/me/mcp-tokens", `{"name":"entry read denied","createRequestId":"additional-token-01"}`, nil, 201)
		var token struct{ Token string }
		readJSON(t, r, &token)
		for _, op := range []struct{ method, path, body string }{{"POST", createPath, body}, {"GET", entryPath, ""}, {"GET", "/api/me/submissions?kind=entry", ""}} {
			f.request(t, 0, additionalActor{}, op.method, op.path, op.body, map[string]string{"Authorization": "Bearer " + token.Token, "Idempotency-Key": "additional-mcp-key-01"}, 403).Body.Close()
		}
	})
	f.deliver(t)
	reviewFile := "/api/admin/reviews/" + entry.ReviewID + "/assets/" + file + "/content"
	t.Run("pending_snapshot_files_and_method_binding", func(t *testing.T) {
		f.request(t, 0, f.bob, "GET", reviewFile, "", nil, 403).Body.Close()
		r := f.request(t, 0, f.admin, "GET", reviewFile, "", nil, 200)
		actual, err := io.ReadAll(r.Body)
		r.Body.Close()
		must(t, err)
		if !bytes.Equal(actual, data) {
			t.Fatal("pending S3 bytes changed")
		}
		f.request(t, 0, f.admin, "GET", strings.Replace(reviewFile, file, otherFile, 1), "", nil, 404).Body.Close()
		_, err = f.gateways[0].GetEntryReviewSnapshot(t.Context(), &contentpb.GetEntryReviewSnapshotRequest{ReviewId: entry.ReviewID})
		grpcCode(t, err, codes.PermissionDenied)
		assertion, err := f.identity.clients[0].ResolvePrincipal(t.Context(), &identitypb.ResolvePrincipalRequest{Audience: "content", FullMethod: contentpb.ContentService_GetMyEntrySubmission_FullMethodName, Credential: &identitypb.ResolvePrincipalRequest_SessionCookie{SessionCookie: f.bob.cookie.Value}})
		must(t, err)
		_, err = f.gateways[1].ResubmitRejectedEntry(t.Context(), &contentpb.ResubmitRejectedEntryRequest{ActorAssertion: assertion.ActorAssertion, EntryId: entry.ID, ExpectedRevision: 1})
		grpcCode(t, err, codes.Unauthenticated)
		_, err = f.senders[0].CreateEntryReviewCase(t.Context(), &moderationpb.CreateEntryReviewCaseRequest{})
		grpcCode(t, err, codes.InvalidArgument)
	})
	f.decide(t, entry.ReviewID, "reject", "fix provenance")
	f.request(t, 0, f.admin, "GET", reviewFile, "", nil, 404).Body.Close()
	var rejected additionalEntryResult
	readJSON(t, f.request(t, 1, f.bob, "GET", entryPath, "", nil, 200), &rejected)
	if rejected.State != "rejected" || rejected.RejectionReason == nil || *rejected.RejectionReason != "fix provenance" {
		t.Fatal("rejected version lost its reason")
	}
	var resubmitted additionalEntryResult
	resubmitBody := `{"expectedRevision":1,"content":` + body + `}`
	readJSON(t, f.request(t, 1, f.bob, "PUT", entryPath, resubmitBody, nil, 200), &resubmitted)
	if resubmitted.ID != entry.ID || resubmitted.Revision != 2 || resubmitted.ReviewID == entry.ReviewID || resubmitted.State != "pending_review" {
		t.Fatal("resubmission overwrote history/identity")
	}
	t.Run("immutable_retry_and_case_digest", func(t *testing.T) {
		old := f.submit(t, 1, f.bob, task.ID, key["Idempotency-Key"], body)
		if old.Revision != 1 || old.ReviewID != entry.ReviewID {
			t.Fatalf("create retry returned wrong immutable version: revision=%d review=%q state=%q", old.Revision, old.ReviewID, old.State)
		}
		f.request(t, 0, f.bob, "POST", createPath, additionalInput("agent", "changed key payload", file), key, 409).Body.Close()
		f.request(t, 0, f.bob, "PUT", entryPath, `{"expectedRevision":1,"content":`+additionalInput("agent", "different retry", file)+`}`, nil, 409).Body.Close()
		snap, err := f.reviewers[0].GetEntryReviewSnapshot(t.Context(), &contentpb.GetEntryReviewSnapshotRequest{ReviewId: resubmitted.ReviewID})
		must(t, err)
		wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(snap.Snapshot)
		must(t, err)
		var persisted string
		must(t, f.identity.db.QueryRow(t.Context(), `SELECT snapshot_hash FROM content.entry_review_requests WHERE id=$1`, resubmitted.ReviewID).Scan(&persisted))
		if persisted != fmt.Sprintf("%x", sha256.Sum256(wire)) {
			t.Fatal("digest differs from deterministic formal Proto")
		}
		_, err = f.reviewers[1].ApplyEntryReviewDecision(t.Context(), &contentpb.ApplyEntryReviewDecisionRequest{DecisionId: "additional-wrong-digest", ReviewId: resubmitted.ReviewID, EntryId: entry.ID, TaskId: task.ID, SubmissionRevision: 2, Outcome: contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE, SnapshotSha256: strings.Repeat("0", 64)})
		grpcCode(t, err, codes.AlreadyExists)
	})
	f.deliver(t)
	f.decide(t, resubmitted.ReviewID, "approve", "PRIVATE_REVIEW_REASON")
	var approved additionalEntryResult
	readJSON(t, f.request(t, 0, f.bob, "GET", entryPath, "", nil, 200), &approved)
	if approved.State != "approved" || approved.ApprovedAt == nil {
		t.Fatal("approval missing authoritative timestamp/state")
	}
	t.Run("approved_projection_file_and_parent_freeze", func(t *testing.T) {
		page := f.publicPage(t, list)
		if len(page.Items) != 3 || page.Items[2].ID != entry.ID || page.Items[2].Author.ID != f.bob.account || page.Items[2].Title != "Bob's frozen work" || page.Items[2].ApprovedAt == nil {
			t.Fatal("public additional entry borrowed parent author/content")
		}
		for i := range initial.Items {
			before, _ := json.Marshal(initial.Items[i])
			after, _ := json.Marshal(page.Items[i])
			if !bytes.Equal(before, after) {
				t.Fatal("additional approval rewrote initial work")
			}
		}
		r := f.request(t, 1, additionalActor{}, "GET", publicFile, "", nil, 200)
		got, err := io.ReadAll(r.Body)
		r.Body.Close()
		must(t, err)
		if !bytes.Equal(got, data) || r.Header.Get("Cache-Control") != "no-store" || r.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("public S3 bytes/headers invalid")
		}
		for _, path := range []string{reviewFile, strings.Replace(publicFile, entry.ID, initial.Items[0].ID, 1), strings.Replace(publicFile, file, otherFile, 1)} {
			f.request(t, 0, f.admin, "GET", path, "", nil, 404).Body.Close()
		}
		if parent() != frozenParent {
			t.Fatal("additional approval changed frozen parent")
		}
	})
	t.Run("same_key_concurrency_and_owner_task_scope", func(t *testing.T) {
		var wg sync.WaitGroup
		results := make(chan additionalEntryResult, 6)
		for i := range 6 {
			wg.Go(func() {
				results <- f.submit(t, i%2, f.bob, task.ID, "additional-concurrent-01", additionalInput("human", "concurrent", ""))
			})
		}
		wg.Wait()
		close(results)
		ids := map[string]bool{}
		for result := range results {
			ids[result.ID] = true
		}
		if len(results) != 0 || len(ids) != 1 {
			t.Fatal("concurrent create produced multiple entries")
		}
		own := f.submit(t, 0, f.alice, task.ID, "additional-concurrent-01", additionalInput("human", "concurrent", ""))
		if ids[own.ID] {
			t.Fatal("create key shared across authors")
		}
		second := f.publish(t, "additional-parent-key-02")
		other := f.submit(t, 1, f.bob, second.ID, key["Idempotency-Key"], body)
		if other.ID == entry.ID {
			t.Fatal("create key shared across tasks")
		}
	})
	t.Run("own_default_states_category_and_cursor_scope", func(t *testing.T) {
		// The mutable task copy must not determine the frozen category filter.
		_, err := f.identity.db.Exec(t.Context(), `UPDATE content.task_drafts SET content=jsonb_set(content,'{category}','"mutable"') WHERE id=$1`, task.ID)
		must(t, err)
		seen, cursor := map[string]bool{}, ""
		for range 10 {
			var page struct {
				Items      []additionalEntryResult
				NextCursor *string
			}
			readJSON(t, f.request(t, 1, f.bob, "GET", "/api/me/submissions?kind=entry&category="+url.QueryEscape("绘画")+"&limit=1&cursor="+url.QueryEscape(cursor), "", nil, 200), &page)
			for _, item := range page.Items {
				if item.AuthorID != f.bob.account || seen[item.ID] || item.Origin != "additional" {
					t.Fatal("own pagination crossed owner/origin or duplicated")
				}
				seen[item.ID] = true
			}
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
		}
		if len(seen) != 3 || !seen[entry.ID] {
			t.Fatal("default kind=entry omitted approved or pending states")
		}
		for _, suffix := range []string{"&state=approved", "&category=other"} {
			f.request(t, 0, f.bob, "GET", "/api/me/submissions?kind=entry&cursor="+url.QueryEscape(cursor)+suffix, "", nil, 400).Body.Close()
		}
		f.request(t, 0, f.alice, "GET", "/api/me/submissions?kind=entry&cursor="+url.QueryEscape(cursor), "", nil, 400).Body.Close()
		for _, q := range []string{"kind=entry&state=draft", "kind=entry&state=published", "kind=cloud", "kind=entry&state=cloud", "kind=entry&limit=0", "kind=entry&cursor=broken"} {
			f.request(t, 0, f.bob, "GET", "/api/me/submissions?"+q, "", nil, 400).Body.Close()
		}
	})
	t.Run("runtime_grants_and_session_revocation", func(t *testing.T) {
		for _, sql := range []string{`UPDATE content.entry_submissions SET author_id='forged' WHERE false`, `UPDATE content.entry_submissions SET origin='initial' WHERE false`, `UPDATE content.entry_review_requests SET snapshot='{}' WHERE false`, `UPDATE content.entry_decision_receipts SET response='{}' WHERE false`, `DELETE FROM content.entry_submissions WHERE false`, `SELECT * FROM moderation.reviews`, `SELECT * FROM asset.assets`, `CREATE TABLE content.denied(x int)`} {
			if _, err := f.contentDB.Exec(t.Context(), sql); err == nil {
				t.Fatal("Content runtime escaped grants", sql)
			}
		}
		f.request(t, 0, f.bob, "POST", "/api/auth/logout", "", nil, 204).Body.Close()
		f.request(t, 1, f.bob, "GET", entryPath, "", nil, 401).Body.Close()
		f.request(t, 1, f.bob, "POST", createPath, body, key, 401).Body.Close()
	})
}

type additionalLostCase struct {
	moderationpb.ModerationServiceClient
	once sync.Once
}

func (c *additionalLostCase) CreateEntryReviewCase(ctx context.Context, r *moderationpb.CreateEntryReviewCaseRequest, opts ...grpc.CallOption) (*moderationpb.CreateEntryReviewCaseResponse, error) {
	out, err := c.ModerationServiceClient.CreateEntryReviewCase(ctx, r, opts...)
	if err != nil {
		return out, err
	}
	lost := false
	c.once.Do(func() { lost = true })
	if lost {
		return nil, status.Error(codes.Unavailable, "response_lost")
	}
	return out, nil
}

type additionalLostDecision struct {
	contentpb.ContentServiceClient
	once sync.Once
}

func (c *additionalLostDecision) ApplyEntryReviewDecision(ctx context.Context, r *contentpb.ApplyEntryReviewDecisionRequest, opts ...grpc.CallOption) (*contentpb.ApplyEntryReviewDecisionResponse, error) {
	out, err := c.ContentServiceClient.ApplyEntryReviewDecision(ctx, r, opts...)
	if err != nil {
		return out, err
	}
	lost := false
	c.once.Do(func() { lost = true })
	if lost {
		return nil, status.Error(codes.Unavailable, "response_lost")
	}
	return out, nil
}

func TestAdditionalEntryResponseLoss(t *testing.T) {
	f := newAdditionalEntryLab(t)
	task := f.publish(t, "additional-recovery-parent")
	body := additionalInput("human", "recovered work", "")
	key := "additional-unknown-create"
	// The real database commits; only its response is lost. Another instance
	// must recover the same ID, snapshot and one outbox item using the same key.
	f.contents[0].DB = publicBrowseLostCommitPool(t, f.contentDB)
	f.request(t, 0, f.bob, "POST", "/api/tasks/"+task.ID+"/entries", body, map[string]string{"Idempotency-Key": key}, 503).Body.Close()
	f.contents[0].DB = f.contentDB
	entry := f.submit(t, 1, f.bob, task.ID, key, body)
	_, err := f.contents[0].DeliverEntryReview(t.Context(), &additionalLostCase{ModerationServiceClient: f.senders[0]})
	grpcCode(t, err, codes.Unavailable)
	var count int
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT count(*) FROM moderation.reviews WHERE object_kind='entry' AND object_id=$1`, entry.ID).Scan(&count))
	if count != 1 {
		t.Fatal("lost response did not persist exactly one case")
	}
	_, err = f.identity.db.Exec(t.Context(), `UPDATE content.entry_review_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, entry.ReviewID)
	must(t, err)
	_, err = f.contents[1].DeliverEntryReview(t.Context(), f.senders[1])
	must(t, err)
	snap, err := f.reviewers[1].GetEntryReviewSnapshot(t.Context(), &contentpb.GetEntryReviewSnapshotRequest{ReviewId: entry.ReviewID})
	must(t, err)
	changed := proto.Clone(snap.Snapshot).(*contentpb.EntryReviewSnapshot)
	changed.Submission.Content.Title = "tampered case"
	_, err = f.senders[1].CreateEntryReviewCase(t.Context(), &moderationpb.CreateEntryReviewCaseRequest{Snapshot: changed})
	grpcCode(t, err, codes.AlreadyExists)
	f.request(t, 0, f.admin, "POST", "/api/admin/reviews/"+entry.ReviewID+"/decision", `{"decision":"approve","reason":"recovery"}`, nil, 202).Body.Close()
	f.moderations[0].Content = &additionalLostDecision{ContentServiceClient: f.reviewers[0]}
	_, err = f.moderations[0].DeliverDecision(t.Context())
	grpcCode(t, err, codes.Unavailable)
	f.moderations[0].Content = f.reviewers[0]
	var approvedAt time.Time
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT approved_at FROM content.reviewed_entries WHERE id=$1`, entry.ID).Scan(&approvedAt))
	_, err = f.identity.db.Exec(t.Context(), `UPDATE moderation.decision_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, entry.ReviewID)
	must(t, err)
	_, err = f.moderations[1].DeliverDecision(t.Context())
	must(t, err)
	var stable bool
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT (SELECT count(*)=1 FROM content.entry_submissions WHERE author_id=$1 AND task_id=$2 AND create_key=$3) AND (SELECT count(*)=1 FROM content.entry_review_outbox WHERE id=$4) AND (SELECT count(*)=1 FROM moderation.reviews WHERE object_id=$5) AND (SELECT count(*)=1 FROM content.entry_decision_receipts WHERE review_id=$4) AND (SELECT approved_at=$6 AND position=32 FROM content.reviewed_entries WHERE id=$5) AND (SELECT next_entry_position=33 AND catalog_revision=2 FROM content.reviewed_tasks WHERE task_id=$2) AND (SELECT delivery_state='applied' FROM moderation.reviews WHERE id=$4)`, f.bob.account, task.ID, key, entry.ReviewID, entry.ID, approvedAt).Scan(&stable))
	if !stable {
		t.Fatal("retry duplicated/reset durable case, allocation, timestamp or receipt")
	}
	var decisionID string
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT decision_id FROM moderation.reviews WHERE id=$1`, entry.ReviewID).Scan(&decisionID))
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(snap.Snapshot)
	must(t, err)
	decision := &contentpb.ApplyEntryReviewDecisionRequest{DecisionId: decisionID, ReviewId: entry.ReviewID, TaskId: task.ID, EntryId: entry.ID, SubmissionRevision: 1, Outcome: contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE, Reason: proto.String("recovery"), SnapshotSha256: fmt.Sprintf("%x", sha256.Sum256(wire))}
	var wg sync.WaitGroup
	errors := make(chan error, 6)
	for i := range 6 {
		wg.Go(func() { _, e := f.reviewers[i%2].ApplyEntryReviewDecision(t.Context(), decision); errors <- e })
	}
	wg.Wait()
	close(errors)
	for e := range errors {
		must(t, e)
	}
	wrong := proto.Clone(decision).(*contentpb.ApplyEntryReviewDecisionRequest)
	wrong.Reason = proto.String("changed")
	_, err = f.reviewers[0].ApplyEntryReviewDecision(t.Context(), wrong)
	grpcCode(t, err, codes.AlreadyExists)
	// Real tightening and exact old decision replay cannot republish the work.
	_, err = f.reviewers[0].ApplyAssetReview(t.Context(), &contentpb.ApplyAssetReviewRequest{DecisionId: "additional-recovery-revoke", TaskId: task.ID, SubmissionRevision: 1, ExpectedRevision: 1, Decision: &contentpb.ApplyAssetReviewRequest_EntryReview{EntryReview: &contentpb.EntryAssetReview{EntryId: entry.ID, State: contentpb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVAL_REVOKED}}})
	must(t, err)
	_, err = f.reviewers[1].ApplyEntryReviewDecision(t.Context(), decision)
	must(t, err)
	if page := f.publicPage(t, "/api/tasks/"+task.ID+"/entries"); len(page.Items) != 2 {
		t.Fatal("old approval revived revoked entry")
	}
	var own additionalEntryResult
	readJSON(t, f.request(t, 0, f.bob, "GET", "/api/me/entries/"+entry.ID, "", nil, 200), &own)
	if own.State != "approval_revoked" {
		t.Fatal("owner state did not follow revocation")
	}
}

func TestAdditionalEntryPagesBeyondInitialBundle(t *testing.T) {
	f := newAdditionalEntryLab(t)
	task := f.publish(t, "additional-pages-parent")
	list := "/api/tasks/" + task.ID + "/entries"
	first := f.publicPage(t, list+"?limit=1")
	if first.NextCursor == nil {
		t.Fatal("initial v1 cursor missing")
	}
	expected := map[string]bool{}
	for _, item := range f.publicPage(t, list).Items {
		expected[item.ID] = true
	}
	for i := range 33 {
		entry := f.submit(t, i%2, f.bob, task.ID, fmt.Sprintf("additional-pages-%04d", i), additionalInput("human", fmt.Sprintf("additional-%02d", i), ""))
		f.deliver(t)
		f.decide(t, entry.ReviewID, "approve", "page approval")
		expected[entry.ID] = true
	}
	// A v1 initial cursor remains valid after adding >=32 catalog positions.
	if page := f.publicPage(t, list+"?limit=1&cursor="+url.QueryEscape(*first.NextCursor)); len(page.Items) != 1 {
		t.Fatal("old v1 cursor invalidated by addition")
	}
	seen, cursor, sawV2 := map[string]bool{}, "", false
	for range 20 {
		page := f.publicPage(t, list+"?limit=3&cursor="+url.QueryEscape(cursor))
		for _, item := range page.Items {
			if seen[item.ID] || !expected[item.ID] {
				t.Fatal("public keyset duplicated or invented entries")
			}
			seen[item.ID] = true
			if item.Author.ID != f.alice.account && item.Author.ID != f.bob.account {
				t.Fatal("public author lost")
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		must(t, err)
		var c struct {
			V        int   `json:"v"`
			Position int32 `json:"position"`
		}
		must(t, json.Unmarshal(raw, &c))
		if c.Position >= 32 {
			if c.V != 2 {
				t.Fatal("additional position used v1 cursor")
			}
			sawV2 = true
			var malformed map[string]any
			must(t, json.Unmarshal(raw, &malformed))
			malformed["v"] = 1
			bad, err := json.Marshal(malformed)
			must(t, err)
			f.request(t, 0, additionalActor{}, "GET", list+"?cursor="+base64.RawURLEncoding.EncodeToString(bad), "", nil, 400).Body.Close()
		}
	}
	if len(seen) != 35 || !sawV2 {
		t.Fatal("paging did not traverse the >32 mixed catalog")
	}
}
