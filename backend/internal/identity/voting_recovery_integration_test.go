//go:build integration

package identity

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/voting"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Forward real Content RPC, then stop at the former check/write race window.
// The barrier controls scheduling, not the authority or database outcome.
type votingAuthorityBarrier struct {
	contentpb.ContentServiceClient
	once             sync.Once
	checked, release chan struct{}
}

func (b *votingAuthorityBarrier) GetVotingCatalogSnapshot(ctx context.Context, r *contentpb.GetVotingCatalogSnapshotRequest, o ...grpc.CallOption) (*contentpb.GetVotingCatalogSnapshotResponse, error) {
	out, err := b.ContentServiceClient.GetVotingCatalogSnapshot(ctx, r, o...)
	if err == nil {
		b.once.Do(func() {
			close(b.checked)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return out, err
}
func votingAssertion(t *testing.T, f *additionalEntryLab, a additionalActor, method string) string {
	t.Helper()
	out, err := f.identity.clients[0].ResolvePrincipal(t.Context(), &identitypb.ResolvePrincipalRequest{Credential: &identitypb.ResolvePrincipalRequest_SessionCookie{SessionCookie: a.cookie.Value}, Audience: "voting", FullMethod: method, Origin: f.identity.origin, CsrfToken: a.csrf})
	must(t, err)
	return out.ActorAssertion
}
func votingTakedown(t *testing.T, f *additionalEntryLab, task, key string) error {
	t.Helper()
	var source, display int64
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT source_revision,revision FROM content.reviewed_tasks WHERE task_id=$1`, task).Scan(&source, &display))
	_, err := f.reviewers[1].ApplyAssetReview(t.Context(), &contentpb.ApplyAssetReviewRequest{TaskId: task, DecisionId: key, SubmissionRevision: source, ExpectedRevision: display, Decision: &contentpb.ApplyAssetReviewRequest_TaskState{TaskState: contentpb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN}})
	return err
}
func TestVotingFenceRejectsValidatedOldRequest(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-race-task-01")
	b := votingBallotFor(t, f, f.bob, task.ID)
	barrier := &votingAuthorityBarrier{ContentServiceClient: f.catalogs[0], checked: make(chan struct{}), release: make(chan struct{})}
	f.votes[0].Content = barrier
	req := &votingpb.CastVoteRequest{ActorAssertion: votingAssertion(t, f, f.bob, votingpb.VotingService_CastVote_FullMethodName), TaskId: task.ID, BallotId: b.ID, EntryId: b.Entries[0].ID}
	done := make(chan error, 1)
	go func() { _, err := f.voteClients[0].CastVote(t.Context(), req); done <- err }()
	select {
	case <-barrier.checked:
	case <-time.After(5 * time.Second):
		t.Fatal("authority barrier not reached")
	}
	must(t, votingTakedown(t, f, task.ID, "voting-race-takedown-01"))
	close(barrier.release)
	if err := <-done; status.Code(err) != codes.Aborted {
		t.Fatal("old validated request crossed committed freeze", err)
	}
	var n int
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT count(*) FROM voting.current_votes WHERE task_id=$1`, task.ID).Scan(&n))
	if n != 0 {
		t.Fatal("stale vote persisted")
	}
}
func TestVotingPermanentIdentityAndRecovery(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-id-task-01")
	base := "/api/tasks/" + task.ID
	var token struct {
		Token      string
		Credential struct{ ID string }
	}
	votingRead(t, f.request(t, 0, f.bob, "POST", "/api/me/mcp-tokens", `{"name":"voting","createRequestId":"voting-token-01"}`, nil, 201), &token)
	headers := map[string]string{"Authorization": "Bearer " + token.Token}
	f.request(t, 1, additionalActor{}, "GET", base+"/ballot", "", headers, 403).Body.Close()
	f.request(t, 1, additionalActor{}, "PUT", base+"/vote", `{}`, headers, 403).Body.Close()
	f.request(t, 1, f.bob, "GET", base+"/voting-eligibility", "", headers, 400).Body.Close()
	b := votingBallotFor(t, f, f.bob, task.ID)
	assertion := votingAssertion(t, f, f.bob, votingpb.VotingService_GetBallot_FullMethodName)
	_, err := f.voteClients[0].GetVotingEligibility(t.Context(), &votingpb.GetVotingEligibilityRequest{ActorAssertion: assertion, TaskId: task.ID})
	grpcCode(t, err, codes.Unauthenticated)
	wrong := &identitypb.ResolvePrincipalRequest{Audience: "content", FullMethod: votingpb.VotingService_GetBallot_FullMethodName, Credential: &identitypb.ResolvePrincipalRequest_SessionCookie{SessionCookie: f.bob.cookie.Value}}
	_, err = f.identity.clients[0].ResolvePrincipal(t.Context(), wrong)
	grpcCode(t, err, codes.PermissionDenied)
	_, err = f.voteClients[0].GetBallot(t.Context(), &votingpb.GetBallotRequest{ActorAssertion: "forged", TaskId: task.ID})
	grpcCode(t, err, codes.Unauthenticated)
	// Real PG commits; a network wrapper drops COMMIT acknowledgement.
	lost := publicBrowseLostCommitPool(t, f.votingDB)
	f.votes[0].DB = lost
	r := &votingpb.CastVoteRequest{ActorAssertion: votingAssertion(t, f, f.bob, votingpb.VotingService_CastVote_FullMethodName), TaskId: task.ID, BallotId: b.ID, EntryId: b.Entries[0].ID}
	_, err = f.voteClients[0].CastVote(t.Context(), r)
	if err == nil {
		t.Fatal("injected lost COMMIT was not observed")
	}
	first, err := f.voteClients[1].CastVote(t.Context(), r)
	must(t, err)
	if first.Vote.Revision != 1 {
		t.Fatal("unknown commit duplicated vote")
	}
	// Closure confirmation uses a fresh real connection and permits statistics only
	// once it observes the committed permanent row, despite lost acknowledgement.
	var stats votingStats
	votingRead(t, f.request(t, 0, f.bob, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200), &stats)
	closed := stats.Eligibility.ClosedAt
	f.votes[0].DB = f.votingDB
	for i := range f.votes {
		*f.votes[i] = voting.Server{DB: f.votingDB, Identity: f.votes[i].Identity, Content: f.catalogs[i]}
	}
	var eligibility struct {
		CanVote, PermanentlyClosed bool
		ClosedAt                   string
	}
	votingRead(t, f.request(t, 1, additionalActor{}, "GET", base+"/voting-eligibility", "", headers, 200), &eligibility)
	if eligibility.CanVote || !eligibility.PermanentlyClosed || eligibility.ClosedAt != closed {
		t.Fatal("MCP account closure diverged")
	}
	votingRead(t, f.request(t, 1, additionalActor{}, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":true}`, headers, 200), &stats)
	if stats.Eligibility.ClosedAt != closed || stats.TotalVotes != 1 {
		t.Fatal("cross credential closure changed")
	}
	cookie, me := f.identity.login(t, "additional-bob", nil)
	relogin := additionalActor{cookie, me.Account.Id, me.CsrfToken}
	f.request(t, 1, relogin, "GET", base+"/ballot", "", nil, 403).Body.Close()
	// Rotating MCP tokens is a new credential, not a new stable account.
	var rotated struct{ Token string }
	votingRead(t, f.request(t, 0, relogin, "POST", "/api/me/mcp-tokens", `{"name":"voting-rotated","createRequestId":"voting-token-02"}`, nil, 201), &rotated)
	var rotatedEligibility struct {
		PermanentlyClosed bool
		ClosedAt          string
	}
	votingRead(t, f.request(t, 1, additionalActor{}, "GET", base+"/voting-eligibility", "", map[string]string{"Authorization": "Bearer " + rotated.Token}, 200), &rotatedEligibility)
	if !rotatedEligibility.PermanentlyClosed || rotatedEligibility.ClosedAt != closed {
		t.Fatal("rotated token restored voting")
	}

	votingRead(t, f.request(t, 1, f.admin, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200), &stats)
	f.request(t, 0, f.admin, "GET", base+"/ballot", "", nil, 403).Body.Close()
	// Runtime cannot undo either permanent fact or service isolation.
	for _, sql := range []string{`DELETE FROM voting.statistics_access`, `UPDATE voting.statistics_access SET closed_at=clock_timestamp()`, `TRUNCATE voting.statistics_access`, `CREATE TABLE voting.illegal(id int)`, `SELECT * FROM identity.accounts`, `SELECT * FROM content.task_drafts`, `UPDATE voting.account_task_guards SET account_id='forged'`} {
		if _, err = f.votingDB.Exec(t.Context(), sql); err == nil {
			t.Fatalf("runtime allowed %s", sql)
		}
	}
	f.request(t, 0, f.bob, "DELETE", "/api/me/mcp-tokens/"+token.Credential.ID, "", nil, 204).Body.Close()
	f.request(t, 1, additionalActor{}, "GET", base+"/voting-eligibility", "", headers, 401).Body.Close()
	f.request(t, 0, relogin, "POST", "/api/auth/logout", "", nil, 204).Body.Close()
	f.request(t, 1, relogin, "GET", base+"/voting-eligibility", "", nil, 401).Body.Close()
}

func TestVotingLockCancellationAndCommitRollback(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-lock-task-01")
	b := votingBallotFor(t, f, f.bob, task.ID)
	req := &votingpb.CastVoteRequest{ActorAssertion: votingAssertion(t, f, f.bob, votingpb.VotingService_CastVote_FullMethodName), TaskId: task.ID, BallotId: b.ID, EntryId: b.Entries[0].ID}
	lock, err := f.votingDB.Begin(t.Context())
	must(t, err)
	defer lock.Rollback(context.Background())
	_, err = lock.Exec(t.Context(), `SELECT account_id FROM voting.account_task_guards WHERE account_id=$1 AND task_id=$2 FOR UPDATE`, f.bob.account, task.ID)
	must(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err = f.voteClients[0].CastVote(ctx, req)
	grpcCode(t, err, codes.DeadlineExceeded)
	must(t, lock.Rollback(t.Context()))
	_, err = f.identity.db.Exec(t.Context(), `CREATE FUNCTION voting.fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure before commit'; END $$; CREATE CONSTRAINT TRIGGER fail_commit AFTER INSERT ON voting.current_votes DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION voting.fail_commit()`)
	must(t, err)
	_, err = f.voteClients[0].CastVote(t.Context(), req)
	if err == nil {
		t.Fatal("precommit failure accepted")
	}
	var n int
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT count(*) FROM voting.current_votes`).Scan(&n))
	if n != 0 {
		t.Fatal("partial vote survived rollback")
	}
	_, err = f.identity.db.Exec(t.Context(), `DROP TRIGGER fail_commit ON voting.current_votes;DROP FUNCTION voting.fail_commit()`)
	must(t, err)
	_, err = f.voteClients[1].CastVote(t.Context(), req)
	must(t, err)
	_, err = f.identity.db.Exec(t.Context(), `CREATE FUNCTION voting.fail_close_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected closure rollback'; END $$; CREATE CONSTRAINT TRIGGER fail_close_commit AFTER INSERT ON voting.statistics_access DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION voting.fail_close_commit()`)
	must(t, err)
	f.request(t, 0, f.bob, "POST", "/api/tasks/"+task.ID+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 503).Body.Close()
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT count(*) FROM voting.statistics_access`).Scan(&n))
	if n != 0 {
		t.Fatal("rolled back closure persisted")
	}
	_, err = f.identity.db.Exec(t.Context(), `DROP TRIGGER fail_close_commit ON voting.statistics_access;DROP FUNCTION voting.fail_close_commit()`)
	must(t, err)
	// Gate SHARE held by an already admitted operation blocks freeze. A cancelled
	// freeze releases its connection and never changes the committed generation.
	lock, err = f.votingDB.Begin(t.Context())
	must(t, err)
	defer lock.Rollback(context.Background())
	var generation int64
	must(t, lock.QueryRow(t.Context(), `SELECT generation FROM voting.catalog_gates WHERE task_id=$1 FOR SHARE`, task.ID).Scan(&generation))
	prepare := &votingpb.PrepareCatalogChangeRequest{TaskId: task.ID, ChangeId: "chg_cccccccccccccccccccccccccccccccc", ExpectedGeneration: generation, Generation: generation + 1}
	ctx, cancel = context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err = f.contents[0].Voting.PrepareCatalogChange(ctx, prepare)
	grpcCode(t, err, codes.DeadlineExceeded)
	must(t, lock.Rollback(t.Context()))
	_, err = f.contents[1].Voting.PrepareCatalogChange(t.Context(), prepare)
	must(t, err)
	_, err = f.voteClients[1].CastVote(t.Context(), req)
	grpcCode(t, err, codes.Aborted)
}

func TestVotingEmptyCatalogAndMigration(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-empty-task-01")
	b := votingBallotFor(t, f, f.bob, task.ID)
	for i, e := range b.Entries {
		var rev, source int64
		must(t, f.contentDB.QueryRow(t.Context(), `SELECT revision,source_revision FROM content.reviewed_tasks WHERE task_id=$1`, task.ID).Scan(&rev, &source))
		_, err := f.reviewers[0].ApplyAssetReview(t.Context(), &contentpb.ApplyAssetReviewRequest{TaskId: task.ID, DecisionId: fmt.Sprintf("voting-empty-revoke-%02d", i), ExpectedRevision: rev, SubmissionRevision: source, Decision: &contentpb.ApplyAssetReviewRequest_EntryReview{EntryReview: &contentpb.EntryAssetReview{EntryId: e.ID, State: contentpb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVAL_REVOKED}}})
		must(t, err)
	}
	empty := votingBallotFor(t, f, f.bob, task.ID)
	if len(empty.Entries) != 0 {
		t.Fatal("not complete empty ballot")
	}
	var stats votingStats
	votingRead(t, f.request(t, 0, f.bob, "POST", "/api/tasks/"+task.ID+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200), &stats)
	if stats.TotalVotes != 0 || len(stats.EntryVotes) != 0 || stats.AgentVoteShare != nil {
		t.Fatal("empty statistics fabricated")
	}
	// Re-run and tamper detection against a real schema, not migration mocks.
	must(t, voting.Migrate(t.Context(), f.identity.db))
	must(t, voting.Migrate(t.Context(), f.identity.db))
	var checksum string
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT checksum FROM voting.schema_migrations LIMIT 1`).Scan(&checksum))
	_, err := f.identity.db.Exec(t.Context(), `UPDATE voting.schema_migrations SET checksum='tampered'`)
	must(t, err)
	if voting.Migrate(t.Context(), f.identity.db) == nil {
		t.Fatal("changed checksum accepted")
	}
	_, err = f.identity.db.Exec(t.Context(), `UPDATE voting.schema_migrations SET checksum=$1`, checksum)
	must(t, err)
	// No implicit GET statistics endpoint or query/account override.
	f.request(t, 0, f.bob, "GET", "/api/tasks/"+task.ID+"/statistics-access", "", nil, http.StatusMethodNotAllowed).Body.Close()
	f.request(t, 0, f.bob, "GET", "/api/tasks/"+task.ID+"/voting-eligibility?accountId=forged", "", nil, 400).Body.Close()
}

// Fault-injection wrappers never fabricate authority or a success result. They
// call real mTLS Voting first, then discard one response at a protocol boundary.
type votingLostProtocolResponse struct {
	votingpb.VotingServiceClient
	prepare, activate sync.Once
}

func (c *votingLostProtocolResponse) PrepareCatalogChange(ctx context.Context, r *votingpb.PrepareCatalogChangeRequest, o ...grpc.CallOption) (*votingpb.PrepareCatalogChangeResponse, error) {
	out, err := c.VotingServiceClient.PrepareCatalogChange(ctx, r, o...)
	if err != nil {
		return out, err
	}
	drop := false
	c.prepare.Do(func() { drop = true })
	if drop {
		return nil, status.Error(codes.Unavailable, "injected_response_loss")
	}
	return out, nil
}
func (c *votingLostProtocolResponse) ActivateCatalogSnapshot(ctx context.Context, r *votingpb.ActivateCatalogSnapshotRequest, o ...grpc.CallOption) (*votingpb.ActivateCatalogSnapshotResponse, error) {
	out, err := c.VotingServiceClient.ActivateCatalogSnapshot(ctx, r, o...)
	if err != nil {
		return out, err
	}
	drop := false
	c.activate.Do(func() { drop = true })
	if drop {
		return nil, status.Error(codes.Unavailable, "injected_response_loss")
	}
	return out, nil
}
func TestVotingCatalogDeliveryRecovery(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-outbox-task-01")
	original := f.contents[0].Voting
	loss := &votingLostProtocolResponse{VotingServiceClient: original}
	f.contents[0].Voting = loss
	f.contents[1].Voting = loss
	err := votingTakedown(t, f, task.ID, "voting-outbox-takedown-01")
	grpcCode(t, err, codes.Unavailable)
	var stage, gate string
	var intent string
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT id,stage FROM content.voting_catalog_changes WHERE task_id=$1 AND stage!='completed'`, task.ID).Scan(&intent, &stage))
	if stage != "queued" {
		t.Fatal("lost prepare skipped queued phase")
	}
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT state FROM voting.catalog_gates WHERE task_id=$1`, task.ID).Scan(&gate))
	if gate != "frozen" {
		t.Fatal("lost response reopened gate")
	}
	f.request(t, 1, additionalActor{}, "GET", "/api/tasks/"+task.ID, "", nil, 404).Body.Close()
	// Accelerate lease expiry in the isolated DB; next worker still executes the
	// checked-in claim/token protocol, not a test-only delivery implementation.
	_, err = f.identity.db.Exec(t.Context(), `UPDATE content.voting_catalog_changes SET available_at=clock_timestamp() WHERE id=$1`, intent)
	must(t, err)
	_, err = f.contents[0].DeliverCatalog(t.Context())
	grpcCode(t, err, codes.Unavailable)
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT stage FROM content.voting_catalog_changes WHERE id=$1`, intent).Scan(&stage))
	if stage != "applied" {
		t.Fatal("lost activate did not preserve local result")
	}
	_, err = f.identity.db.Exec(t.Context(), `UPDATE content.voting_catalog_changes SET available_at=clock_timestamp() WHERE id=$1`, intent)
	must(t, err)
	f.contents[1].Voting = original
	out, err := f.contents[1].DeliverCatalog(t.Context())
	must(t, err)
	if out.Outcome != "delivered" {
		t.Fatal(out)
	}
	// Replaying exact old intent recovers response without allocating generation.
	must(t, votingTakedownReplay(t, f, task.ID, "voting-outbox-takedown-01"))
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT state FROM voting.catalog_gates WHERE task_id=$1`, task.ID).Scan(&gate))
	if gate != "closed" {
		t.Fatal("tombstone lost")
	}
	var n int
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT count(*) FROM content.voting_catalog_changes WHERE task_id=$1`, task.ID).Scan(&n))
	if n != 2 {
		t.Fatal("replay duplicated catalog change", n)
	}
}
func votingTakedownReplay(t *testing.T, f *additionalEntryLab, task, key string) error {
	t.Helper()
	var source, display int64
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT source_revision,revision FROM content.reviewed_tasks WHERE task_id=$1`, task).Scan(&source, &display))
	_, err := f.reviewers[0].ApplyAssetReview(t.Context(), &contentpb.ApplyAssetReviewRequest{TaskId: task, DecisionId: key, SubmissionRevision: source, ExpectedRevision: display - 1, Decision: &contentpb.ApplyAssetReviewRequest_TaskState{TaskState: contentpb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN}})
	return err
}
func TestVotingExistingCatalogBootstrapRetry(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	clients := [2]votingpb.VotingServiceClient{f.contents[0].Voting, f.contents[1].Voting}
	for i := range f.contents {
		f.contents[i].Voting = nil
	}
	task := f.publish(t, "voting-legacy-bootstrap-01")
	for i := range f.contents {
		f.contents[i].Voting = clients[i]
	}
	f.request(t, 0, f.bob, "GET", "/api/tasks/"+task.ID+"/ballot", "", nil, 503).Body.Close()
	f.contents[0].Voting = &votingLostProtocolResponse{VotingServiceClient: clients[0]}
	if f.contents[0].BootstrapCatalogs(t.Context()) == nil {
		t.Fatal("failed bootstrap reported success")
	}
	_, err := f.identity.db.Exec(t.Context(), `UPDATE content.voting_catalog_changes SET available_at=clock_timestamp() WHERE stage='queued'`)
	must(t, err)
	// A fresh instance retries the pending bootstrap instead of skipping a head
	// merely because its first attempt inserted that head before losing response.
	must(t, f.contents[1].BootstrapCatalogs(t.Context()))
	must(t, f.contents[1].BootstrapCatalogs(t.Context()))
	b := votingBallotFor(t, f, f.bob, task.ID)
	if len(b.Entries) != 2 || b.Generation != 1 {
		t.Fatal("bootstrap did not recover complete directory")
	}
}

type votingActivationBarrier struct {
	votingpb.VotingServiceClient
	activated, release chan struct{}
	once               sync.Once
}

func (b *votingActivationBarrier) ActivateCatalogSnapshot(ctx context.Context, r *votingpb.ActivateCatalogSnapshotRequest, o ...grpc.CallOption) (*votingpb.ActivateCatalogSnapshotResponse, error) {
	out, err := b.VotingServiceClient.ActivateCatalogSnapshot(ctx, r, o...)
	if err == nil {
		b.once.Do(func() {
			close(b.activated)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return out, err
}
func TestVotingCatalogStaleWorkerCannotConfirmNewLease(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-lease-task-01")
	barrier := &votingActivationBarrier{VotingServiceClient: f.contents[1].Voting, activated: make(chan struct{}), release: make(chan struct{})}
	f.contents[1].Voting = barrier
	t.Cleanup(func() {
		select {
		case <-barrier.release:
		default:
			close(barrier.release)
		}
	})
	done := make(chan error, 1)
	go func() { done <- votingTakedown(t, f, task.ID, "voting-lease-takedown-01") }()
	select {
	case <-barrier.activated:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach activation barrier")
	}
	var oldLease string
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT lease_token FROM content.voting_catalog_changes WHERE task_id=$1 AND stage='applied'`, task.ID).Scan(&oldLease))
	_, err := f.identity.db.Exec(t.Context(), `UPDATE content.voting_catalog_changes SET available_at=clock_timestamp() WHERE task_id=$1 AND stage='applied'`, task.ID)
	must(t, err)
	result, err := f.contents[0].DeliverCatalog(t.Context())
	must(t, err)
	if result.Outcome != "delivered" {
		t.Fatal("new worker did not recover activation", result)
	}
	close(barrier.release)
	must(t, <-done)
	var gen int64
	var active *string
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT generation,active_change FROM content.voting_catalog_heads WHERE task_id=$1`, task.ID).Scan(&gen, &active))
	if gen != 2 || active != nil || oldLease == "" {
		t.Fatal("stale confirmation corrupted completed head")
	}
}

func TestVotingInternalTrustAndBudget(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-trust-task-01")
	assertion := votingAssertion(t, f, f.bob, votingpb.VotingService_GetBallot_FullMethodName)
	for _, caller := range []string{"content", "asset", "challenge"} {
		client := votingpb.NewVotingServiceClient(serviceTestGRPC(t, f.identity, "voting", caller, voting.Authorization, func(g *grpc.Server) { votingpb.RegisterVotingServiceServer(g, f.votes[0]) }))
		_, err := client.GetBallot(t.Context(), &votingpb.GetBallotRequest{TaskId: task.ID, ActorAssertion: assertion})
		grpcCode(t, err, codes.PermissionDenied)
		_, err = client.GetHumanPreferenceSnapshot(t.Context(), &votingpb.GetHumanPreferenceSnapshotRequest{})
		if caller == "challenge" {
			grpcCode(t, err, codes.InvalidArgument)
		} else {
			grpcCode(t, err, codes.PermissionDenied)
		}
		_, err = client.GetHumanPreferenceSnapshot(t.Context(), &votingpb.GetHumanPreferenceSnapshotRequest{RunId: "run_internal_trust_001", Purpose: "training"})
		if caller == "challenge" {
			grpcCode(t, err, codes.FailedPrecondition) // No eligible human votes.
		} else {
			grpcCode(t, err, codes.PermissionDenied)
		}
	}
	// Fail required checks after an earlier allowed ballot. No cached authority or
	// principal may substitute for either required service check.
	_ = votingBallotFor(t, f, f.bob, task.ID)
	identity, authority := f.votes[1].Identity, f.votes[1].Content
	f.votes[1].Identity = nil
	f.request(t, 1, f.bob, "GET", "/api/tasks/"+task.ID+"/ballot", "", nil, 503).Body.Close()
	f.votes[1].Identity = identity
	f.votes[1].Content = nil
	f.request(t, 1, f.bob, "GET", "/api/tasks/"+task.ID+"/ballot", "", nil, 503).Body.Close()
	f.votes[1].Content = authority
	// An isolated historical projection fixture is intentionally too large for
	// complete HTTP encoding. This is a real Content SQL/mapper+gRPC budget check,
	// not 320 independently reviewed business submissions.
	var author string
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT author_id FROM content.task_drafts WHERE id=$1`, task.ID).Scan(&author))
	raw := additionalInput("human", strings.Repeat("<", 1200), "")
	_, err := f.identity.db.Exec(t.Context(), `INSERT INTO content.reviewed_entries(id,task_id,position,state,origin,author_id,content) SELECT 'ent_'||lpad(to_hex(n),32,'0'),$1,32+n,'approved','additional',$2,$3::jsonb FROM generate_series(1,320)n`, task.ID, author, raw)
	must(t, err)
	_, err = f.catalogs[0].GetVotingCatalogSnapshot(t.Context(), &contentpb.GetVotingCatalogSnapshotRequest{TaskId: task.ID})
	grpcCode(t, err, codes.ResourceExhausted)
	response := f.request(t, 1, f.bob, "GET", "/api/tasks/"+task.ID+"/ballot", "", nil, 503)
	var problem struct{ Code string }
	votingRead(t, response, &problem)
	if problem.Code != "catalog_too_large" {
		t.Fatal("catalog budget did not expose stable error", problem.Code)
	}
}
