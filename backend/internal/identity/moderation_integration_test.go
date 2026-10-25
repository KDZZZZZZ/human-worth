//go:build integration

package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/repo/postgres"
	identitygrpc "github.com/KDZZZZZZ/human-worth/backend/internal/identity/transport/grpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/moderation"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Fault injection forwards to REAL mTLS Content/Moderation first, then loses
// the successful response. It is not a replacement authorization/DB service.
type lostCaseResponse struct {
	moderationpb.ModerationServiceClient
	once sync.Once
}

func (c *lostCaseResponse) CreateReviewCase(ctx context.Context, r *moderationpb.CreateReviewCaseRequest, opts ...grpc.CallOption) (*moderationpb.CreateReviewCaseResponse, error) {
	out, err := c.ModerationServiceClient.CreateReviewCase(ctx, r, opts...)
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

type lostDecisionResponse struct {
	contentpb.ContentServiceClient
	once sync.Once
}

type blockedDecisionResponse struct{ contentpb.ContentServiceClient }

func (blockedDecisionResponse) ApplyReviewDecision(context.Context, *contentpb.ApplyReviewDecisionRequest, ...grpc.CallOption) (*contentpb.ApplyReviewDecisionResponse, error) {
	return nil, status.Error(codes.FailedPrecondition, "submission_revision_conflict")
}

type channelLogWriter struct{ records chan string }

func (w channelLogWriter) Write(p []byte) (int, error) {
	w.records <- string(p)
	return len(p), nil
}

func (c *lostDecisionResponse) ApplyReviewDecision(ctx context.Context, r *contentpb.ApplyReviewDecisionRequest, opts ...grpc.CallOption) (*contentpb.ApplyReviewDecisionResponse, error) {
	out, err := c.ContentServiceClient.ApplyReviewDecision(ctx, r, opts...)
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
func TestModerationHTTPOutbox(t *testing.T) {
	l := newIdentityLab(t)
	adb := serviceTestDatabase(t, l, "asset", asset.Migrate, "../../../ops/asset-grants.sql")
	cdb := serviceTestDatabase(t, l, "content", content.Migrate, "../../../ops/content/grants.sql")
	mdb := serviceTestDatabase(t, l, "moderation", moderation.Migrate, "../../../ops/moderation/grants.sql")
	t.Run("migration_checksum_and_repeat", func(t *testing.T) {
		must(t, moderation.Migrate(t.Context(), l.db))
		var checksum string
		must(t, l.db.QueryRow(t.Context(), `SELECT checksum FROM moderation.schema_migrations WHERE version='001_moderation.sql'`).Scan(&checksum))
		defer func() {
			_, err := l.db.Exec(context.Background(), `UPDATE moderation.schema_migrations SET checksum=$1 WHERE version='001_moderation.sql'`, checksum)
			must(t, err)
		}()
		_, err := l.db.Exec(t.Context(), `UPDATE moderation.schema_migrations SET checksum='tampered' WHERE version='001_moderation.sql'`)
		must(t, err)
		if moderation.Migrate(t.Context(), l.db) == nil {
			t.Fatal("migration accepted changed checksum")
		}
	})
	cs := &content.Server{DB: cdb, Identity: l.client(t, 0, "content", l.pki)}
	contentClient := func(caller string) contentpb.ContentServiceClient {
		return contentpb.NewContentServiceClient(serviceTestGRPC(t, l, "content", caller, content.Authorization, func(g *grpc.Server) { contentpb.RegisterContentServiceServer(g, cs) }))
	}
	moderatorContent := contentClient("moderation")
	as := layeredAssetServer(adb, asset.FileStorage{Root: t.TempDir()}, l.client(t, 0, "asset", l.pki), contentClient("asset"))
	assetClient := func(caller string) assetpb.AssetServiceClient {
		return assetpb.NewAssetServiceClient(serviceTestGRPC(t, l, "asset", caller, asset.Authorization, func(g *grpc.Server) { assetpb.RegisterAssetServiceServer(g, as) }))
	}
	cs.Asset = assetClient("content")
	ms := &moderation.Server{DB: mdb, Identity: l.client(t, 0, "moderation", l.pki), Content: moderatorContent}
	moderationClient := func(caller string) moderationpb.ModerationServiceClient {
		return moderationpb.NewModerationServiceClient(serviceTestGRPC(t, l, "moderation", caller, moderation.Authorization, func(g *grpc.Server) { moderationpb.RegisterModerationServiceServer(g, ms) }))
	}
	sender := moderationClient("content")
	gwModeration := moderationClient("gateway")
	gwContent := contentClient("gateway")
	gwAsset := assetClient("gateway")
	for i := range l.web {
		h, err := gateway.New(l.clients[i], gateway.Options{Origin: l.origin, Content: gwContent, Asset: gwAsset, Moderation: gwModeration, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		must(t, err)
		l.web[i].Config.Handler = h
	}
	alice, me := l.login(t, "review-alice", nil)
	bob, bme := l.login(t, "review-bob", nil)
	_, firstAdmin := l.login(t, "review-admin", nil)
	adminOps := application.NewAdmin(postgres.New(l.db), randomToken)
	must(t, identitygrpc.Error(adminOps.ChangeAccount(t.Context(), "test-operator", dto.ChangeAccountInput{AccountID: firstAdmin.Account.Id, Role: "admin", State: "active", ExpectedVersion: 1})))
	admin, ame := l.login(t, "review-admin", nil)
	headers := func(csrf string) map[string]string {
		return map[string]string{"Origin": l.origin, "X-CSRF-Token": csrf, "Content-Type": "application/json"}
	}
	ah, bh, mh := headers(me.CsrfToken), headers(bme.CsrfToken), headers(ame.CsrfToken)
	request := func(cookie *http.Cookie, method, path, body string, h map[string]string) *http.Response {
		var cookies []*http.Cookie
		if cookie != nil {
			cookies = []*http.Cookie{cookie}
		}
		return l.request(t, 0, method, path, cookies, body, h)
	}
	draft := func(assetID string) string {
		return `{"title":"review task","summary":"","description":"body","entries":[{"side":"human","title":"human","source":"self","artifacts":[{"kind":"file","assetId":"` + assetID + `"}],"permissions":{"cloudUse":false}},{"side":"agent","title":"agent","source":"model","artifacts":[{"kind":"text","text":"complete agent work"},{"kind":"link","url":"https://example.com/work"}],"permissions":{"cloudUse":false},"agentConfiguration":{"model":"test"}}]}`
	}
	data := []byte("administrator can read only the exact pending snapshot\n")
	ah["Idempotency-Key"] = "moderation-upload-0001"
	r := request(alice, "POST", "/api/me/assets/upload-sessions", fmt.Sprintf(`{"filename":"review.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%x"}`, len(data), sha256.Sum256(data)), ah)
	httpStatus(t, r, 201)
	var upload struct {
		Asset         struct{ ID string }
		UploadSession struct{ ID string }
	}
	readJSON(t, r, &upload)
	chunk := headers(me.CsrfToken)
	chunk["Content-Type"] = "application/octet-stream"
	chunk["Content-Range"] = fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data))
	httpStatus(t, request(alice, "PUT", "/api/me/assets/upload-sessions/"+upload.UploadSession.ID+"/content", string(data), chunk), 200)
	httpStatus(t, request(alice, "POST", "/api/me/assets/"+upload.Asset.ID+"/complete", `{"uploadSessionId":"`+upload.UploadSession.ID+`"}`, ah), 200)
	ah["Idempotency-Key"] = "moderation-draft-0001"
	r = request(alice, "POST", "/api/tasks", draft(upload.Asset.ID), ah)
	httpStatus(t, r, 201)
	var task draftResult
	readJSON(t, r, &task)
	path := "/api/me/tasks/" + task.ID + "/review-requests"
	t.Run("author_identity_and_bundle", func(t *testing.T) {
		httpStatus(t, request(nil, "POST", path, `{"expectedRevision":1}`, ah), 401)
		httpStatus(t, request(bob, "POST", path, `{"expectedRevision":1}`, bh), 404)
		httpStatus(t, request(alice, "POST", path, `{"expectedRevision":99}`, ah), 409)
		httpStatus(t, request(alice, "POST", path, `{"expectedRevision":1,"authorId":"forged"}`, ah), 400)
		for i, side := range []string{"empty", "human", "agent"} {
			var parsed map[string]any
			must(t, json.Unmarshal([]byte(draft(upload.Asset.ID)), &parsed))
			entries := make([]any, 0)
			for _, entry := range parsed["entries"].([]any) {
				if entry.(map[string]any)["side"] == side {
					entries = append(entries, entry)
				}
			}
			parsed["entries"] = entries
			body, err := json.Marshal(parsed)
			must(t, err)
			ah["Idempotency-Key"] = fmt.Sprintf("incomplete-draft-%04d", i)
			rr := request(alice, "POST", "/api/tasks", string(body), ah)
			httpStatus(t, rr, 201)
			var d draftResult
			readJSON(t, rr, &d)
			httpStatus(t, request(alice, "POST", "/api/me/tasks/"+d.ID+"/review-requests", `{"expectedRevision":1}`, ah), 422)
		}
		_, err := l.db.Exec(t.Context(), `UPDATE asset.assets SET state='failed' WHERE id=$1`, upload.Asset.ID)
		must(t, err)
		httpStatus(t, request(alice, "POST", path, `{"expectedRevision":1}`, ah), 409)
		var frozen int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM content.review_requests WHERE task_id=$1`, task.ID).Scan(&frozen))
		if frozen != 0 {
			t.Fatal("failed asset check partially submitted")
		}
		_, err = l.db.Exec(t.Context(), `UPDATE asset.assets SET state='ready' WHERE id=$1`, upload.Asset.ID)
		must(t, err)

		httpStatus(t, request(alice, "GET", "/api/admin/reviews", "", nil), 403)
		httpStatus(t, request(nil, "GET", "/api/admin/reviews", "", nil), 401)
	})
	r = request(alice, "POST", path, `{"expectedRevision":1}`, ah)
	httpStatus(t, r, 200)
	readJSON(t, r, &task)
	if task.State != "pending_review" || task.ReviewID == nil {
		t.Fatal("not pending")
	}
	reviewID := *task.ReviewID
	t.Run("snapshot_and_case_idempotency", func(t *testing.T) {
		for range 2 {
			rr := request(alice, "POST", path, `{"expectedRevision":1}`, ah)
			httpStatus(t, rr, 200)
			var same draftResult
			readJSON(t, rr, &same)
			if same.ReviewID == nil || *same.ReviewID != reviewID {
				t.Fatal("duplicate review")
			}
		}
		httpStatus(t, request(alice, "PUT", "/api/me/tasks/"+task.ID, `{"expectedRevision":1,"content":`+draft(upload.Asset.ID)+`}`, ah), 409)
		// Moderation unavailable: durable pending state/outbox survive, no case yet.
		lost := &lostCaseResponse{ModerationServiceClient: sender}
		_, err := cs.DeliverReview(t.Context(), lost)
		grpcCode(t, err, codes.Unavailable)
		var count int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM moderation.reviews WHERE id=$1`, reviewID).Scan(&count))
		if count != 1 {
			t.Fatal("case not persisted before lost response")
		}
		_, err = l.db.Exec(t.Context(), `UPDATE content.review_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, reviewID)
		must(t, err)
		// Another Content instance takes over the expired durable lease.
		cs2 := &content.Server{DB: cdb}
		_, err = cs2.DeliverReview(t.Context(), sender)
		must(t, err)
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM moderation.reviews WHERE task_id=$1`, task.ID).Scan(&count))
		if count != 1 {
			t.Fatal("duplicate case on retry")
		}
		snap, err := moderatorContent.GetReviewSnapshot(t.Context(), &contentpb.GetReviewSnapshotRequest{ReviewId: reviewID})
		must(t, err)
		_, err = moderationClient("gateway").CreateReviewCase(t.Context(), &moderationpb.CreateReviewCaseRequest{Snapshot: snap.Snapshot})
		grpcCode(t, err, codes.PermissionDenied)
		changed := proto.Clone(snap.Snapshot).(*contentpb.ReviewSnapshot)
		changed.Submission.Content.Title = "changed"
		_, err = sender.CreateReviewCase(t.Context(), &moderationpb.CreateReviewCaseRequest{Snapshot: changed})
		grpcCode(t, err, codes.AlreadyExists)
	})
	t.Run("concurrent_opposite_first_decisions", func(t *testing.T) {
		body := strings.ReplaceAll(draft(upload.Asset.ID), `{"kind":"file","assetId":"`+upload.Asset.ID+`"}`, `{"kind":"text","text":"alternate human work"}`)
		ah["Idempotency-Key"] = "review-concurrency-0001"
		rr := request(alice, "POST", "/api/tasks", body, ah)
		httpStatus(t, rr, 201)
		var d draftResult
		readJSON(t, rr, &d)
		rr = request(alice, "POST", "/api/me/tasks/"+d.ID+"/review-requests", `{"expectedRevision":1}`, ah)
		httpStatus(t, rr, 200)
		readJSON(t, rr, &d)
		_, err := cs.DeliverReview(t.Context(), sender)
		must(t, err)
		// Both pending cases must be reachable through successive real HTTP pages.
		seen, cursor := map[string]bool{}, ""
		for range 3 {
			pageResponse := request(admin, "GET", "/api/admin/reviews?limit=1&cursor="+cursor, "", nil)
			httpStatus(t, pageResponse, 200)
			var page struct {
				Items      []struct{ ID string }
				NextCursor *string
			}
			readJSON(t, pageResponse, &page)
			if len(page.Items) != 1 || seen[page.Items[0].ID] {
				t.Fatal("pending review page lost/duplicated a case")
			}
			seen[page.Items[0].ID] = true
			if page.NextCursor == nil {
				break
			}
			cursor = *page.NextCursor
			httpStatus(t, request(admin, "GET", "/api/admin/reviews?status=approved&cursor="+cursor, "", nil), 400)
		}
		if len(seen) != 2 || !seen[reviewID] || !seen[*d.ReviewID] {
			t.Fatal("not all pending cases returned")
		}
		responses := make(chan int, 2)
		var wg sync.WaitGroup
		for i, body := range []string{`{"decision":"approve"}`, `{"decision":"reject","reason":"not enough evidence"}`} {
			wg.Go(func() {
				r := l.request(t, i, "POST", "/api/admin/reviews/"+*d.ReviewID+"/decision", []*http.Cookie{admin}, body, mh)
				responses <- r.StatusCode
				r.Body.Close()
			})
		}
		wg.Wait()
		a, b := <-responses, <-responses
		if !((a == 202 && b == 409) || (a == 409 && b == 202)) {
			t.Fatalf("opposite first decisions: %d %d", a, b)
		}
		_, err = ms.DeliverDecision(t.Context())
		must(t, err)
		var count int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM content.review_decision_receipts WHERE review_id=$1`, *d.ReviewID).Scan(&count))
		if count != 1 {
			t.Fatal("concurrent decisions applied twice")
		}
	})
	assetPath := "/api/admin/reviews/" + reviewID + "/assets/" + upload.Asset.ID + "/content"
	t.Run("pending_files_require_admin_exact_snapshot", func(t *testing.T) {
		httpStatus(t, request(alice, "GET", assetPath, "", nil), 403)
		httpStatus(t, request(nil, "GET", assetPath, "", map[string]string{"X-Role": "admin"}), 401)
		rr := request(admin, "GET", assetPath, "", nil)
		httpStatus(t, rr, 200)
		actual, err := io.ReadAll(rr.Body)
		rr.Body.Close()
		must(t, err)
		if !bytes.Equal(actual, data) {
			t.Fatal("review bytes mismatch")
		}
		httpStatus(t, request(admin, "GET", strings.Replace(assetPath, upload.Asset.ID, "ast_"+strings.Repeat("0", 32), 1), "", nil), 404)
		httpStatus(t, request(admin, "GET", "/api/admin/reviews/"+reviewID+"/assets/"+upload.Asset.ID+"/content?ownerId=forged", "", nil), 400)
	})
	t.Run("admin_list_and_concurrent_decision", func(t *testing.T) {
		rr := request(admin, "GET", "/api/admin/reviews?limit=1", "", nil)
		httpStatus(t, rr, 200)
		for _, q := range []string{"limit=0", "limit=1&limit=2", "cursor=broken", "status=unknown", "ownerId=forged"} {
			httpStatus(t, request(admin, "GET", "/api/admin/reviews?"+q, "", nil), 400)
		}
		httpStatus(t, request(admin, "POST", "/api/admin/reviews/"+reviewID+"/decision", `{"decision":"reject"}`, mh), 400)
		httpStatus(t, request(admin, "POST", "/api/admin/reviews/"+reviewID+"/decision", `{"decision":"reject","reason":"fix source"}`, nil), 403)
		responses := make(chan int, 2)
		var wg sync.WaitGroup
		// Opposite decisions race; only one durable intent. Force reject as first
		// decision below, then race its identical retry with approve.
		rr = request(admin, "POST", "/api/admin/reviews/"+reviewID+"/decision", `{"decision":"reject","reason":"fix source"}`, mh)
		httpStatus(t, rr, 202)
		for _, body := range []string{`{"decision":"approve"}`, `{"decision":"reject","reason":"fix source"}`} {
			wg.Go(func() {
				r := request(admin, "POST", "/api/admin/reviews/"+reviewID+"/decision", body, mh)
				responses <- r.StatusCode
				r.Body.Close()
			})
		}
		wg.Wait()
		a, b := <-responses, <-responses
		if !((a == 202 && b == 409) || (a == 409 && b == 202)) {
			t.Fatalf("decisions %d %d", a, b)
		}
		ms.Content = &lostDecisionResponse{ContentServiceClient: moderatorContent}
		_, err := ms.DeliverDecision(t.Context())
		grpcCode(t, err, codes.Unavailable)
		var state, delivery string
		must(t, l.db.QueryRow(t.Context(), `SELECT state FROM content.task_drafts WHERE id=$1`, task.ID).Scan(&state))
		must(t, l.db.QueryRow(t.Context(), `SELECT delivery_state FROM moderation.reviews WHERE id=$1`, reviewID).Scan(&delivery))
		if state != "rejected" || delivery != "pending" {
			t.Fatal("lost receipt semantics wrong")
		}
		_, err = l.db.Exec(t.Context(), `UPDATE moderation.decision_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, reviewID)
		must(t, err)
		ms2 := &moderation.Server{DB: mdb, Content: moderatorContent}
		_, err = ms2.DeliverDecision(t.Context())
		must(t, err)
		rr = request(admin, "POST", "/api/admin/reviews/"+reviewID+"/decision", `{"decision":"reject","reason":"fix source"}`, mh)
		httpStatus(t, rr, 200)
		httpStatus(t, request(admin, "GET", assetPath, "", nil), 404)
	})
	var oldDecision string
	must(t, l.db.QueryRow(t.Context(), `SELECT decision_id FROM moderation.reviews WHERE id=$1`, reviewID).Scan(&oldDecision))
	t.Run("rejected_edit_resubmit_approve_and_old_replay", func(t *testing.T) {
		rr := request(alice, "PUT", "/api/me/tasks/"+task.ID, `{"expectedRevision":1,"content":`+draft(upload.Asset.ID)+`}`, ah)
		httpStatus(t, rr, 200)
		readJSON(t, rr, &task)
		if task.Revision != 2 || task.State != "draft" {
			t.Fatal("not new editable revision")
		}
		rr = request(alice, "POST", path, `{"expectedRevision":2}`, ah)
		httpStatus(t, rr, 200)
		readJSON(t, rr, &task)
		newID := *task.ReviewID
		if newID == reviewID {
			t.Fatal("history overwritten")
		}
		old, err := moderatorContent.ApplyReviewDecision(t.Context(), &contentpb.ApplyReviewDecisionRequest{DecisionId: oldDecision, ReviewId: reviewID, TaskId: task.ID, SubmissionRevision: 1, Outcome: contentpb.ReviewOutcome_REVIEW_OUTCOME_REJECT, Reason: proto.String("fix source")})
		must(t, err)
		if old.State != "rejected" {
			t.Fatal("old receipt changed")
		}
		_, err = cs.DeliverReview(t.Context(), sender)
		must(t, err)
		approval := `{"decision":"approve","reason":"材料完整，审核通过"}`
		httpStatus(t, request(admin, "POST", "/api/admin/reviews/"+newID+"/decision", approval, mh), 202)
		ms.Content = moderatorContent
		_, err = ms.DeliverDecision(t.Context())
		must(t, err)
		var decisionID string
		must(t, l.db.QueryRow(t.Context(), `SELECT decision_id FROM moderation.reviews WHERE id=$1`, newID).Scan(&decisionID))
		approved := &contentpb.ApplyReviewDecisionRequest{DecisionId: decisionID, ReviewId: newID, TaskId: task.ID, SubmissionRevision: 2, Outcome: contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE, Reason: proto.String("材料完整，审核通过")}
		receipt, err := moderatorContent.ApplyReviewDecision(t.Context(), approved)
		must(t, err)
		httpStatus(t, request(admin, "POST", "/api/admin/reviews/"+newID+"/decision", approval, mh), 200)
		httpStatus(t, request(admin, "POST", "/api/admin/reviews/"+newID+"/decision", `{"decision":"approve","reason":"different"}`, mh), 409)
		var storedReason string
		must(t, l.db.QueryRow(t.Context(), `SELECT reason FROM moderation.reviews WHERE id=$1`, newID).Scan(&storedReason))
		if storedReason != "材料完整，审核通过" {
			t.Fatal("approval reason not persisted")
		}
		var result struct{ Reason *string }
		readJSON(t, request(admin, "GET", "/api/admin/reviews/"+newID, "", nil), &result)
		if result.Reason == nil || *result.Reason != storedReason {
			t.Fatal("approval reason not returned")
		}
		if len(receipt.Entries) != 2 {
			t.Fatal("no stable entries")
		}
		publicPath := "/api/tasks/" + task.ID + "/entries/" + receipt.Entries[0].Id + "/assets/" + upload.Asset.ID + "/content"
		rr = request(nil, "GET", publicPath, "", nil)
		httpStatus(t, rr, 200)
		actual, err := io.ReadAll(rr.Body)
		rr.Body.Close()
		must(t, err)
		if !bytes.Equal(actual, data) {
			t.Fatal("approved bytes changed")
		}
		_, err = moderatorContent.ApplyAssetReview(t.Context(), &contentpb.ApplyAssetReviewRequest{DecisionId: "formal-takedown-00001", TaskId: task.ID, SubmissionRevision: 2, ExpectedRevision: 1, Decision: &contentpb.ApplyAssetReviewRequest_TaskState{TaskState: contentpb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN}})
		must(t, err)
		replay, err := moderatorContent.ApplyReviewDecision(t.Context(), approved)
		must(t, err)
		if !proto.Equal(receipt, replay) {
			t.Fatal("unstable execution receipt")
		}
		httpStatus(t, request(nil, "GET", publicPath, "", nil), 404)
		_, err = moderatorContent.ApplyAssetReview(t.Context(), &contentpb.ApplyAssetReviewRequest{DecisionId: "formal-reopen-0000001", TaskId: task.ID, SubmissionRevision: 2, ExpectedRevision: 2, Decision: &contentpb.ApplyAssetReviewRequest_TaskState{TaskState: contentpb.ReviewedTaskState_REVIEWED_TASK_STATE_PUBLISHED}})
		grpcCode(t, err, codes.FailedPrecondition)
		var n int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM content.review_requests WHERE task_id=$1`, task.ID).Scan(&n))
		if n != 2 {
			t.Fatal("history not preserved")
		}
	})
	t.Run("blocked_delivery_logs_error_after_persisting_state", func(t *testing.T) {
		blockedHeaders := make(map[string]string, len(ah)+1)
		for key, value := range ah {
			blockedHeaders[key] = value
		}
		blockedHeaders["Idempotency-Key"] = "blocked-review-flow-0001"
		rr := request(alice, "POST", "/api/tasks", draft(upload.Asset.ID), blockedHeaders)
		httpStatus(t, rr, 201)
		var blockedTask draftResult
		readJSON(t, rr, &blockedTask)
		rr = request(alice, "POST", "/api/me/tasks/"+blockedTask.ID+"/review-requests", `{"expectedRevision":1}`, blockedHeaders)
		httpStatus(t, rr, 200)
		readJSON(t, rr, &blockedTask)
		if _, err := cs.DeliverReview(t.Context(), sender); err != nil {
			t.Fatal("deliver review:", err)
		}
		rr = request(admin, "POST", "/api/admin/reviews/"+*blockedTask.ReviewID+"/decision", `{"decision":"approve"}`, mh)
		httpStatus(t, rr, 202)

		records := make(chan string, 1)
		logger := slog.New(slog.NewJSONHandler(channelLogWriter{records: records}, nil))
		ms.Content = blockedDecisionResponse{ContentServiceClient: moderatorContent}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { ms.RunDelivery(ctx, logger); close(done) }()
		var record string
		select {
		case record = <-records:
		case <-time.After(5 * time.Second):
			cancel()
			<-done
			t.Fatal("blocked result was not logged")
		}
		cancel()
		<-done
		var fields map[string]any
		if err := json.Unmarshal([]byte(record), &fields); err != nil {
			t.Fatal("invalid structured log:", err)
		}
		if fields["level"] != "ERROR" || fields["msg"] != "outbox_delivery_blocked" || fields["outcome"] != "blocked" || fields["delivery"] != "moderation_to_content" || fields["item_id"] != *blockedTask.ReviewID || fields["stage"] != "downstream_business_conflict" {
			t.Fatalf("wrong blocked log: %v", fields)
		}
		var deliveryState string
		var blocked bool
		if err := l.db.QueryRow(t.Context(), `SELECT r.delivery_state,o.blocked FROM moderation.reviews r JOIN moderation.decision_outbox o ON o.id=r.id WHERE r.id=$1`, *blockedTask.ReviewID).Scan(&deliveryState, &blocked); err != nil {
			t.Fatal(err)
		}
		if deliveryState != "blocked" || !blocked {
			t.Fatalf("ERROR was logged before blocked state persisted: %s/%t", deliveryState, blocked)
		}
	})
	t.Run("runtime_boundaries_and_revocation", func(t *testing.T) {
		for _, sql := range []string{`SELECT * FROM identity.accounts`, `SELECT * FROM content.task_drafts`, `SELECT * FROM asset.assets`, `CREATE TABLE moderation.forbidden(x int)`, `UPDATE moderation.reviews SET snapshot='{}'`, `DELETE FROM moderation.reviews`, `UPDATE moderation.schema_migrations SET checksum='x'`} {
			if _, err := mdb.Exec(t.Context(), sql); err == nil {
				t.Fatalf("runtime escaped: %s", sql)
			}
		}
		httpStatus(t, request(admin, "POST", "/api/auth/logout", "", mh), 204)
		httpStatus(t, request(admin, "GET", "/api/admin/reviews", "", nil), 401)
	})
}

func TestModerationApprovalMigration(t *testing.T) {
	l := newIdentityLab(t)
	old, err := os.ReadFile("../moderation/migrations/001_moderation.sql")
	must(t, err)
	_, err = l.db.Exec(t.Context(), `CREATE SCHEMA moderation; CREATE TABLE moderation.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp());`+string(old))
	must(t, err)
	_, err = l.db.Exec(t.Context(), `INSERT INTO moderation.schema_migrations(version,checksum) VALUES('001_moderation.sql',$1)`, fmt.Sprintf("%x", sha256.Sum256(old)))
	must(t, err)
	_, err = l.db.Exec(t.Context(), `INSERT INTO moderation.reviews(id,task_id,submission_revision,snapshot,snapshot_hash,created_at) VALUES('pending','task-pending',1,'{}',decode(repeat('00',32),'hex'),clock_timestamp());
INSERT INTO moderation.reviews(id,task_id,submission_revision,snapshot,snapshot_hash,created_at,status,decision,decision_id,reason,reviewer_id,decided_at,delivery_state,receipt) VALUES
('approved','task-approved',1,'{}',decode(repeat('00',32),'hex'),clock_timestamp(),'approved','approve','decision-approved',NULL,'admin',clock_timestamp(),'applied','{}'),
('rejected','task-rejected',1,'{}',decode(repeat('00',32),'hex'),clock_timestamp(),'rejected','reject','decision-rejected','原驳回理由','admin',clock_timestamp(),'applied','{}');`)
	must(t, err)
	if moderation.Ready(t.Context(), l.db) == nil {
		t.Fatal("old schema accepted as ready")
	}
	if _, err = l.db.Exec(t.Context(), `UPDATE moderation.reviews SET reason='材料完整，审核通过' WHERE id='approved'`); err == nil {
		t.Fatal("old constraint no longer reproduces approval reason failure")
	}
	must(t, moderation.Migrate(t.Context(), l.db))
	must(t, moderation.Migrate(t.Context(), l.db))
	must(t, moderation.Ready(t.Context(), l.db))
	var n int
	must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM moderation.reviews WHERE (id='approved' AND reason IS NULL) OR (id='rejected' AND reason='原驳回理由') OR (id='pending' AND reason IS NULL)`).Scan(&n))
	if n != 3 {
		t.Fatal("upgrade changed prior decisions")
	}
	_, err = l.db.Exec(t.Context(), `UPDATE moderation.reviews SET reason='材料完整，审核通过' WHERE id='approved'`)
	must(t, err)
	if _, err = l.db.Exec(t.Context(), `UPDATE moderation.reviews SET reason=NULL WHERE id='rejected'`); err == nil {
		t.Fatal("rejection reason constraint lost")
	}
	if _, err = l.db.Exec(t.Context(), `UPDATE moderation.reviews SET reason='undecided reason' WHERE id='pending'`); err == nil {
		t.Fatal("undecided reason constraint lost")
	}
	if _, err = l.db.Exec(t.Context(), `UPDATE moderation.reviews SET reason=repeat('x',4001) WHERE id='approved'`); err == nil {
		t.Fatal("approval reason byte limit lost")
	}
}
