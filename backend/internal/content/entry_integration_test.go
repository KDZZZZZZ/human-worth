//go:build integration

package content

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// These are Content storage/component tests. Identity is deliberately a narrow
// fake; assertions/certificates/OAuth and complete HTTPS authorization are covered
// by the real-Identity integration suite. No persistence or COMMIT is mocked.
type entryStorageIdentity struct {
	identitypb.IdentityServiceClient
}

func (entryStorageIdentity) VerifyActor(_ context.Context, r *identitypb.VerifyActorRequest, _ ...grpc.CallOption) (*identitypb.VerifyActorResponse, error) {
	switch r.ActorAssertion {
	case "alice", "bob":
		return &identitypb.VerifyActorResponse{Principal: &identitypb.Principal{AccountId: r.ActorAssertion, ClientKind: identitypb.ClientKind_CLIENT_KIND_WEB_SESSION}}, nil
	case "mcp":
		return &identitypb.VerifyActorResponse{Principal: &identitypb.Principal{AccountId: "alice", ClientKind: identitypb.ClientKind_CLIENT_KIND_MCP_READ}}, nil
	default:
		return nil, status.Error(codes.Unauthenticated, "test_assertion_revoked")
	}
}

type entryLab struct {
	admin, owner, runtime *pgxpool.Pool
	s                     *Server
}

func entryMust(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func entryCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if status.Code(err) != code {
		t.Fatalf("code=%s want=%s: %v", status.Code(err), code, err)
	}
}
func entryExec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	_, err := db.Exec(t.Context(), sql, args...)
	entryMust(t, err)
}
func entryBool(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	var ok bool
	entryMust(t, db.QueryRow(t.Context(), sql, args...).Scan(&ok))
	if !ok {
		t.Fatal("durable state invariant failed", sql)
	}
}

func entryServiceContext(parent context.Context, name string) context.Context {
	u, _ := url.Parse("spiffe://human-worth/services/" + name)
	cert := &x509.Certificate{URIs: []*url.URL{u}}
	return peer.NewContext(parent, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}}})
}

func newEntryLab(t *testing.T, upgrade bool) *entryLab {
	t.Helper()
	var dsn string
	for _, prefix := range []string{"ASSET", "IDENTITY"} {
		if path := os.Getenv(prefix + "_TEST_DATABASE_URL_FILE"); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read dedicated PG configuration: %T", err)
			}
			dsn = strings.TrimSpace(string(data))
		} else {
			dsn = strings.TrimSpace(os.Getenv(prefix + "_TEST_DATABASE_URL"))
		}
		if dsn != "" {
			break
		}
	}
	if dsn == "" {
		t.Skip("INCONCLUSIVE: dedicated ASSET/IDENTITY_TEST_DATABASE_URL[_FILE] missing")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse dedicated PG configuration: %T", err)
	}
	admin, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("connect dedicated PG: %T", err)
	}
	t.Cleanup(admin.Close)
	if err = admin.Ping(t.Context()); err != nil {
		t.Fatalf("dedicated PG unavailable: %T", err)
	}
	suffix := make([]byte, 12)
	_, err = rand.Read(suffix)
	entryMust(t, err)
	name := "hw_entry_" + hex.EncodeToString(suffix)
	owner, runtime := name+"_owner", name+"_runtime"
	quotedName, quotedOwner, quotedRuntime := pgx.Identifier{name}.Sanitize(), pgx.Identifier{owner}.Sanitize(), pgx.Identifier{runtime}.Sanitize()
	entryExec(t, admin, "CREATE DATABASE "+quotedName)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, e := admin.Exec(ctx, "DROP DATABASE "+quotedName+" WITH (FORCE)")
		if e != nil {
			t.Errorf("drop isolated PG database: %T", e)
		}
		_, e = admin.Exec(ctx, "DROP ROLE IF EXISTS "+quotedRuntime+"; DROP ROLE IF EXISTS "+quotedOwner)
		if e != nil {
			t.Errorf("drop isolated PG roles: %T", e)
		}
	})
	cfg = cfg.Copy()
	cfg.ConnConfig.Database = name
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	entryMust(t, err)
	t.Cleanup(db.Close)
	secret := make([]byte, 24)
	_, err = rand.Read(secret)
	entryMust(t, err)
	password := hex.EncodeToString(secret)
	entryExec(t, db, "REVOKE CREATE ON DATABASE "+quotedName+" FROM PUBLIC")
	entryExec(t, db, "REVOKE CREATE ON SCHEMA public FROM PUBLIC")
	entryExec(t, db, "CREATE ROLE "+quotedOwner+" LOGIN PASSWORD '"+password+"'")
	entryExec(t, db, "CREATE ROLE "+quotedRuntime+" LOGIN PASSWORD '"+password+"'")
	entryExec(t, db, "CREATE SCHEMA content AUTHORIZATION "+quotedOwner)
	entryExec(t, db, "CREATE SCHEMA identity")
	entryExec(t, db, "CREATE TABLE identity.private_data(secret text)")
	entryExec(t, db, "CREATE SCHEMA unrelated")
	entryExec(t, db, "CREATE TABLE unrelated.private_data(secret text)")
	rolePool := func(role string) *pgxpool.Pool {
		c := cfg.Copy()
		c.ConnConfig.User, c.ConnConfig.Password = role, password
		c.MaxConns = 2
		p, e := pgxpool.NewWithConfig(t.Context(), c)
		entryMust(t, e)
		t.Cleanup(p.Close)
		if e = p.Ping(t.Context()); e != nil {
			t.Fatalf("connect isolated PG role: %T", e)
		}
		return p
	}
	l := &entryLab{admin: db, owner: rolePool(owner), runtime: rolePool(runtime)}
	if upgrade {
		entryExec(t, l.owner, `CREATE TABLE content.schema_migrations(version text PRIMARY KEY,checksum text NOT NULL,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`)
		files, e := migrations.ReadDir("migrations")
		entryMust(t, e)
		for _, file := range files {
			if file.Name() >= "006" {
				break
			}
			raw, e := migrations.ReadFile("migrations/" + file.Name())
			entryMust(t, e)
			entryExec(t, l.owner, string(raw))
			entryExec(t, l.owner, `INSERT INTO content.schema_migrations(version,checksum) VALUES($1,$2)`, file.Name(), fmt.Sprintf("%x", sha256.Sum256(raw)))
		}
	} else {
		entryMust(t, Migrate(t.Context(), l.owner))
		entryGrant(t, l)
	}
	l.s = &Server{DB: l.runtime, Identity: entryStorageIdentity{}}
	return l
}

func entryGrant(t *testing.T, l *entryLab) {
	t.Helper()
	raw, err := os.ReadFile("../../../ops/content/grants.sql")
	entryMust(t, err)
	var lines []string
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "\\") {
			lines = append(lines, line)
		}
	}
	entryExec(t, l.admin, strings.ReplaceAll(strings.Join(lines, "\n"), "content_runtime", pgx.Identifier{l.runtime.Config().ConnConfig.User}.Sanitize()))
}

func entrySeedParent(t *testing.T, l *entryLab, n int) string {
	t.Helper()
	task := fmt.Sprintf("tsk_%032x", n)
	input := &pb.TaskDraftInput{Title: "published task", Summary: proto.String("summary"), Description: proto.String("description"), Category: proto.String("绘画"), Entries: []*pb.EntryInput{entryTestInput(t, entryTestHuman)}}
	raw, err := validate(input)
	entryMust(t, err)
	entryExec(t, l.owner, `INSERT INTO content.task_drafts(id,author_id,create_key,create_hash,content,state,revision) VALUES($1,'creator',$2,$3,$4,'published',3)`, task, fmt.Sprintf("initial-create-%016d", n), make([]byte, 32), raw)
	entryExec(t, l.owner, `INSERT INTO content.reviewed_tasks(task_id,source_revision,revision,state,snapshot,updated_at,published_at) VALUES($1,3,7,'published',$2,'2026-01-01','2026-01-02')`, task, raw)
	entryExec(t, l.owner, `INSERT INTO content.reviewed_entries(id,task_id,position,state,approved_at) VALUES($1,$2,0,'approved','2026-01-02')`, fmt.Sprintf("ent_%032x", n), task)
	return task
}
func entrySubmit(t *testing.T, s *Server, task, key string, input *pb.EntryInput) *pb.EntrySubmission {
	t.Helper()
	out, err := s.SubmitAdditionalEntry(serviceContext("gateway"), &pb.SubmitAdditionalEntryRequest{ActorAssertion: "alice", TaskId: task, IdempotencyKey: key, Content: input})
	entryMust(t, err)
	return out.Submission
}
func entryDecision(t *testing.T, l *entryLab, sub *pb.EntrySubmission, key string, approve bool) *pb.ApplyEntryReviewDecisionRequest {
	t.Helper()
	out, err := l.s.GetEntryReviewSnapshot(serviceContext("moderation"), &pb.GetEntryReviewSnapshotRequest{ReviewId: sub.ReviewId})
	entryMust(t, err)
	r := &pb.ApplyEntryReviewDecisionRequest{DecisionId: key, ReviewId: sub.ReviewId, EntryId: sub.Id, TaskId: sub.TaskId, SubmissionRevision: sub.Revision, SnapshotSha256: entrySnapshotDigest(out.Snapshot), Outcome: pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE}
	if !approve {
		r.Outcome = pb.ReviewOutcome_REVIEW_OUTCOME_REJECT
		r.Reason = proto.String("please revise")
	}
	return r
}

func TestEntryIntegrationUpgrade005(t *testing.T) {
	l := newEntryLab(t, true)
	task := entrySeedParent(t, l, 1)
	// Include both known and unknown historical dates and a revoked initial row.
	other := entrySeedParent(t, l, 2)
	entryExec(t, l.owner, `UPDATE content.reviewed_tasks SET published_at=NULL WHERE task_id=$1`, other)
	entryExec(t, l.owner, `UPDATE content.reviewed_entries SET approved_at=NULL,state='approval_revoked' WHERE task_id=$1`, other)
	const oldRows = `SELECT jsonb_build_object('tasks',(SELECT jsonb_agg(to_jsonb(t)-'next_entry_position'-'catalog_revision' ORDER BY task_id) FROM content.reviewed_tasks t),'entries',(SELECT jsonb_agg(to_jsonb(e)-'origin'-'author_id'-'content' ORDER BY id) FROM content.reviewed_entries e),'drafts',(SELECT jsonb_agg(to_jsonb(d) ORDER BY id) FROM content.task_drafts d),'migrations',(SELECT jsonb_agg(to_jsonb(m) ORDER BY version) FROM content.schema_migrations m WHERE version<'006'))`
	var before, after []byte
	entryMust(t, l.owner.QueryRow(t.Context(), oldRows).Scan(&before))
	if Ready(t.Context(), l.owner) == nil {
		t.Fatal("005 accepted as ready")
	}
	entryMust(t, Migrate(t.Context(), l.owner))
	entryMust(t, Migrate(t.Context(), l.owner))
	entryMust(t, Ready(t.Context(), l.owner))
	entryMust(t, l.owner.QueryRow(t.Context(), oldRows).Scan(&after))
	if string(before) != string(after) {
		t.Fatal("006 changed old identities, states, snapshots or historical timestamps")
	}
	entryBool(t, l.owner, `SELECT count(*)=2 AND bool_and(next_entry_position=32 AND catalog_revision=1) FROM content.reviewed_tasks`)
	entryBool(t, l.owner, `SELECT count(*)=2 AND bool_and(origin='initial' AND author_id IS NULL AND content IS NULL) FROM content.reviewed_entries`)
	entryBool(t, l.owner, `SELECT count(*)=8 FROM content.schema_migrations`)
	for _, name := range []string{"005_public_browse.sql", "006_additional_entries.sql"} {
		entryExec(t, l.owner, `UPDATE content.schema_migrations SET checksum='tampered' WHERE version=$1`, name)
		if err := Migrate(t.Context(), l.owner); err == nil || err.Error() != "applied migration changed" {
			t.Fatalf("changed checksum accepted: %v", err)
		}
		raw, err := migrations.ReadFile("migrations/" + name)
		entryMust(t, err)
		entryExec(t, l.owner, `UPDATE content.schema_migrations SET checksum=$2 WHERE version=$1`, name, fmt.Sprintf("%x", sha256.Sum256(raw)))
	}
	entryGrant(t, l)
	entryMust(t, Ready(t.Context(), l.runtime))
	if Migrate(t.Context(), l.runtime) == nil {
		t.Fatal("runtime can migrate")
	}
	for _, sql := range []string{
		`CREATE TABLE content.forbidden(id int)`, `CREATE SCHEMA forbidden`,
		`SELECT * FROM identity.private_data`, `SELECT * FROM unrelated.private_data`,
		`UPDATE content.schema_migrations SET checksum='forged'`,
		`UPDATE content.entry_submissions SET author_id='bob'`, `UPDATE content.entry_submissions SET create_key='another-create-key'`,
		`UPDATE content.entry_review_requests SET snapshot='{}'`, `UPDATE content.entry_review_requests SET content_hash=decode(repeat('00',32),'hex')`,
		`UPDATE content.entry_decision_receipts SET response='{}'`, `UPDATE content.reviewed_entries SET content='{}'`,
		`DELETE FROM content.entry_review_outbox`, `ALTER TABLE content.entry_submissions ADD COLUMN forbidden int`,
	} {
		_, err := l.runtime.Exec(t.Context(), sql)
		var pgerr *pgconn.PgError
		if !errors.As(err, &pgerr) || pgerr.Code != "42501" {
			t.Fatalf("runtime boundary %q: %v", sql, err)
		}
	}
	entryBool(t, l.admin, `SELECT NOT EXISTS(SELECT 1 FROM pg_constraint c JOIN pg_class r ON r.oid=c.conrelid JOIN pg_namespace n ON n.oid=r.relnamespace JOIN pg_class f ON f.oid=c.confrelid JOIN pg_namespace fn ON fn.oid=f.relnamespace WHERE n.nspname='content' AND c.contype='f' AND fn.nspname<>'content')`)
	// Runtime's positive capabilities execute actual writes, not just has_* checks.
	sub := entrySubmit(t, l.s, task, "upgrade-submit-0001", entryTestInput(t, entryTestHuman))
	_, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), entryDecision(t, l, sub, "upgrade-decision-01", true))
	entryMust(t, err)
	entryBool(t, l.owner, `SELECT position=32 AND origin='additional' AND author_id='alice' FROM content.reviewed_entries WHERE id=$1`, sub.Id)
}

func TestEntryIntegrationRevisionAndProjection(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	input := entryTestInput(t, entryTestHuman)
	sub := entrySubmit(t, l.s, task, "revision-create-0001", input)
	reject := entryDecision(t, l, sub, "revision-reject-0001", false)
	_, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), reject)
	entryMust(t, err)
	changed := proto.Clone(input).(*pb.EntryInput)
	changed.Title = "revised work"
	r := &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: changed}
	out, err := l.s.ResubmitRejectedEntry(serviceContext("gateway"), r)
	entryMust(t, err)
	if out.Submission.Revision != 2 || out.Submission.State != "pending_review" || out.Submission.ReviewId == sub.ReviewId {
		t.Fatal("revision not advanced independently")
	}
	approve := entryDecision(t, l, out.Submission, "revision-approve-001", true)
	_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), approve)
	entryMust(t, err)
	// A creation retry returns the immutable original accepted version even after
	// a rejected revision was resubmitted and approved.
	old := entrySubmit(t, l.s, task, "revision-create-0001", input)
	if old.Revision != 1 || old.State != "rejected" || old.Content.GetTitle() != input.GetTitle() {
		t.Fatal("create replay did not return original immutable entry version")
	}
	again, err := l.s.ResubmitRejectedEntry(serviceContext("gateway"), r)
	entryMust(t, err)
	if again.Submission.Revision != 2 || again.Submission.State != "approved" || again.Submission.Content.GetTitle() != changed.GetTitle() {
		t.Fatal("resubmit replay lost committed version")
	}
	r.Content = input
	_, err = l.s.ResubmitRejectedEntry(serviceContext("gateway"), r)
	entryCode(t, err, codes.AlreadyExists)
	_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), entryDecision(t, l, sub, "stale-decision-00001", true))
	entryCode(t, err, codes.Aborted)
	// Owner reads use the actor result on every retry, and hide existence from Bob.
	_, err = l.s.GetMyEntrySubmission(serviceContext("gateway"), &pb.GetMyEntrySubmissionRequest{ActorAssertion: "bob", EntryId: sub.Id})
	entryCode(t, err, codes.NotFound)
	for _, actor := range []string{"revoked", "mcp"} {
		_, err = l.s.SubmitAdditionalEntry(serviceContext("gateway"), &pb.SubmitAdditionalEntryRequest{ActorAssertion: actor, TaskId: task, IdempotencyKey: "revision-create-0001", Content: input})
		want := codes.Unauthenticated
		if actor == "mcp" {
			want = codes.PermissionDenied
		}
		entryCode(t, err, want)
	}
	// Freeze the initial projection even if the private draft changes later.
	entryExec(t, l.owner, `UPDATE content.task_drafts SET content=jsonb_set(content,'{entries,0,title}','"private changed"') WHERE id=$1`, task)
	page, err := l.s.ListApprovedEntries(serviceContext("gateway"), &pb.ListApprovedEntriesRequest{ActorAssertion: "alice", TaskId: task, Limit: 1})
	entryMust(t, err)
	if len(page.Items) != 1 || page.Items[0].Title != "work" || page.Items[0].Author.Id != "creator" || page.NextCursor == "" {
		t.Fatal("initial snapshot projection changed")
	}
	legacy := page.NextCursor
	page, err = l.s.ListApprovedEntries(serviceContext("gateway"), &pb.ListApprovedEntriesRequest{ActorAssertion: "alice", TaskId: task, Limit: 1, Cursor: legacy})
	entryMust(t, err)
	if len(page.Items) != 1 || page.Items[0].Id != sub.Id || page.Items[0].Title != changed.Title || page.Items[0].Author.Id != "alice" {
		t.Fatal("additional projection/legacy cursor incorrect")
	}
	entryBool(t, l.owner, `SELECT source_revision=3 AND revision=7 AND snapshot->'entries'->0->>'title'='work' AND next_entry_position=33 AND catalog_revision=2 AND published_at='2026-01-02' FROM content.reviewed_tasks WHERE task_id=$1`, task)
	// Formal single-entry approval cannot be restored with the legacy shortcut.
	_, err = l.s.ApplyAssetReview(serviceContext("moderation"), &pb.ApplyAssetReviewRequest{DecisionId: "legacy-shortcut-0001", TaskId: task, SubmissionRevision: 3, ExpectedRevision: 7, Decision: &pb.ApplyAssetReviewRequest_EntryReview{EntryReview: &pb.EntryAssetReview{EntryId: sub.Id, State: pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVED}}})
	entryCode(t, err, codes.FailedPrecondition)
}

func TestEntryIntegrationLocksLeaseAndLargeOwnerPages(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	// Parent row contention is bounded by the request context; the canceled
	// waiter must return its runtime connection to the pool for the next request.
	lock, err := l.owner.Begin(t.Context())
	entryMust(t, err)
	t.Cleanup(func() { _ = lock.Rollback(context.Background()) })
	_, err = lock.Exec(t.Context(), `SELECT task_id FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE`, task)
	entryMust(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err = l.s.SubmitAdditionalEntry(entryServiceContext(ctx, "gateway"), &pb.SubmitAdditionalEntryRequest{ActorAssertion: "alice", TaskId: task, IdempotencyKey: "locked-submit-00001", Content: entryTestInput(t, entryTestHuman)})
	entryCode(t, err, codes.DeadlineExceeded)
	entryMust(t, lock.Rollback(t.Context()))
	entryMust(t, l.runtime.Ping(t.Context()))
	var one int
	entryMust(t, l.runtime.QueryRow(t.Context(), `SELECT 1`).Scan(&one))
	if one != 1 {
		t.Fatal("runtime pool not usable after canceled lock wait")
	}
	entryBool(t, l.owner, `SELECT count(*)=0 FROM content.entry_submissions`)

	// Create an outbox item, then let a second claimant replace its lease while
	// the first is in the RPC. The first claimant's late confirmation must affect
	// zero rows and leave the new lease unacknowledged.
	sub := entrySubmit(t, l.s, task, "lease-submit-00001", entryTestInput(t, entryTestHuman))
	entryExec(t, l.owner, `UPDATE content.entry_review_outbox SET available_at=clock_timestamp()-interval '1 second' WHERE id=$1`, sub.ReviewId)
	client := entryLeaseClient{onCall: func() {
		entryExec(t, l.owner, `UPDATE content.entry_review_outbox SET lease_token='newer-lease',available_at=clock_timestamp()+interval '15 seconds' WHERE id=$1`, sub.ReviewId)
	}}
	worked, err := l.s.DeliverEntryReview(t.Context(), client)
	entryMust(t, err)
	if !worked {
		t.Fatal("outbox item was not claimed")
	}
	entryBool(t, l.owner, `SELECT lease_token='newer-lease' AND delivered_at IS NULL AND attempts=1 FROM content.entry_review_outbox WHERE id=$1`, sub.ReviewId)

	// Populate oversized historical rows directly (the normal write validator
	// intentionally caps a single submission at 12 KB) to exercise the response
	// byte budget independently of the 100-item count limit.
	large := strings.Repeat("x", 30000)
	for i := 0; i < 80; i++ {
		id := fmt.Sprintf("ent_%032x", 1000+i)
		data, e := json.Marshal(map[string]any{"side": "human", "title": large, "source": "legacy", "artifacts": []any{map[string]any{"kind": "link", "url": "https://example.com"}}, "permissions": map[string]any{"cloudUse": false}})
		entryMust(t, e)
		entryExec(t, l.owner, `INSERT INTO content.entry_submissions(id,task_id,author_id,create_key,create_hash,revision,state,content,review_id) VALUES($1,$2,'alice',$3,$4,1,'pending_review',$5,$6)`, id, task, fmt.Sprintf("large-create-%012d", i), make([]byte, 32), data, fmt.Sprintf("rev_%032x", 1000+i))
	}
	page, err := l.s.ListMyEntrySubmissions(serviceContext("gateway"), &pb.ListMyEntrySubmissionsRequest{ActorAssertion: "alice", Limit: 100})
	entryMust(t, err)
	if len(page.Items) >= 80 || len(page.Items) == 0 || page.NextCursor == "" || proto.Size(page) > maxSubmissionPageBytes {
		t.Fatalf("large owner page not byte-bounded: count=%d bytes=%d cursor=%q", len(page.Items), proto.Size(page), page.NextCursor)
	}
	second, err := l.s.ListMyEntrySubmissions(serviceContext("gateway"), &pb.ListMyEntrySubmissionsRequest{ActorAssertion: "alice", Limit: 100, Cursor: page.NextCursor})
	entryMust(t, err)
	seen := map[string]bool{}
	for _, item := range append(append([]*pb.EntrySubmission{}, page.Items...), second.Items...) {
		if seen[item.Id] {
			t.Fatalf("owner pagination duplicated %s", item.Id)
		}
		seen[item.Id] = true
	}
	if len(seen) != 80+1 {
		t.Fatalf("owner cursor skipped rows: got %d want 81", len(seen))
	}
}

type entryLeaseClient struct {
	moderationpb.ModerationServiceClient
	onCall func()
}

func (c entryLeaseClient) CreateEntryReviewCase(_ context.Context, r *moderationpb.CreateEntryReviewCaseRequest, _ ...grpc.CallOption) (*moderationpb.CreateEntryReviewCaseResponse, error) {
	c.onCall()
	return &moderationpb.CreateEntryReviewCaseResponse{Review: &moderationpb.Review{Id: r.Snapshot.ReviewId, EntrySnapshot: r.Snapshot}}, nil
}

// Like identity/public_browse_recovery_integration_test.go: forward real COMMIT,
// read PostgreSQL's response, then discard it and close only this connection.
type entryLostCommitConn struct {
	net.Conn
	drop atomic.Bool
}

func (c *entryLostCommitConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if n > 0 && c.drop.Swap(false) {
		_ = c.Conn.Close()
		return 0, io.ErrUnexpectedEOF
	}
	return n, err
}

type entryCommitTracer struct{ armed *atomic.Bool }

func (tr entryCommitTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if d.SQL == "commit" && tr.armed.Swap(false) {
		conn := c.PgConn().Conn()
		if tls, ok := conn.(interface{ NetConn() net.Conn }); ok {
			conn = tls.NetConn()
		}
		if transport, ok := conn.(*entryLostCommitConn); ok {
			transport.drop.Store(true)
		}
	}
	return ctx
}
func (entryCommitTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
func entryLostCommitPool(t *testing.T, original *pgxpool.Pool) (*pgxpool.Pool, *atomic.Bool) {
	t.Helper()
	cfg := original.Config()
	armed := &atomic.Bool{}
	cfg.ConnConfig.Tracer = entryCommitTracer{armed: armed}
	dial := cfg.ConnConfig.DialFunc
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &entryLostCommitConn{Conn: conn}, nil
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	entryMust(t, err)
	t.Cleanup(pool.Close)
	return pool, armed
}

func entryFailCommit(t *testing.T, l *entryLab, table string) func() {
	t.Helper()
	entryExec(t, l.owner, `CREATE FUNCTION content.entry_fail_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected entry commit failure'; END $$`)
	entryExec(t, l.owner, `CREATE CONSTRAINT TRIGGER entry_fail_commit AFTER INSERT ON content.`+table+` DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION content.entry_fail_commit()`)
	return func() {
		entryExec(t, l.owner, `DROP TRIGGER entry_fail_commit ON content.`+table+`; DROP FUNCTION content.entry_fail_commit()`)
	}
}

func TestEntryIntegrationCommitRecovery(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	input := entryTestInput(t, entryTestHuman)
	request := &pb.SubmitAdditionalEntryRequest{ActorAssertion: "alice", TaskId: task, IdempotencyKey: "commit-create-00001", Content: input}
	clear := entryFailCommit(t, l, "entry_review_outbox")
	_, err := l.s.SubmitAdditionalEntry(serviceContext("gateway"), request)
	entryCode(t, err, codes.Unavailable)
	entryBool(t, l.owner, `SELECT (SELECT count(*) FROM content.entry_submissions)+(SELECT count(*) FROM content.entry_review_requests)+(SELECT count(*) FROM content.entry_review_outbox)=0`)
	clear()
	uncertain, armed := entryLostCommitPool(t, l.runtime)
	faulty := &Server{DB: uncertain, Identity: l.s.Identity}
	armed.Store(true)
	_, err = faulty.SubmitAdditionalEntry(serviceContext("gateway"), request)
	entryCode(t, err, codes.Unavailable)
	entryBool(t, l.owner, `SELECT (SELECT count(*) FROM content.entry_submissions)=1 AND (SELECT count(*) FROM content.entry_review_requests)=1 AND (SELECT count(*) FROM content.entry_review_outbox)=1`)
	sub := entrySubmit(t, l.s, task, request.IdempotencyKey, input)
	decision := entryDecision(t, l, sub, "commit-reject-00001", false)
	_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), decision)
	entryMust(t, err)
	revised := proto.Clone(input).(*pb.EntryInput)
	revised.Title = "second version"
	resubmit := &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: revised}
	clear = entryFailCommit(t, l, "entry_review_outbox")
	_, err = l.s.ResubmitRejectedEntry(serviceContext("gateway"), resubmit)
	entryCode(t, err, codes.Unavailable)
	entryBool(t, l.owner, `SELECT revision=1 AND state='rejected' AND content->>'title'='work' AND (SELECT count(*)=1 FROM content.entry_review_requests) AND (SELECT count(*)=1 FROM content.entry_review_outbox) FROM content.entry_submissions WHERE id=$1`, sub.Id)
	clear()
	armed.Store(true)
	_, err = faulty.ResubmitRejectedEntry(serviceContext("gateway"), resubmit)
	entryCode(t, err, codes.Unavailable)
	out, err := l.s.ResubmitRejectedEntry(serviceContext("gateway"), resubmit)
	entryMust(t, err)
	if out.Submission.Revision != 2 {
		t.Fatal("lost resubmit COMMIT created another revision")
	}
	entryBool(t, l.owner, `SELECT (SELECT count(*) FROM content.entry_review_requests)=2 AND (SELECT count(*) FROM content.entry_review_outbox)=2`)
	decision = entryDecision(t, l, out.Submission, "commit-approve-0001", true)
	clear = entryFailCommit(t, l, "entry_decision_receipts")
	_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), decision)
	entryCode(t, err, codes.Unavailable)
	entryBool(t, l.owner, `SELECT s.state='pending_review' AND s.approved_at IS NULL AND r.state='pending_review' AND NOT EXISTS(SELECT 1 FROM content.reviewed_entries WHERE id=s.id) AND NOT EXISTS(SELECT 1 FROM content.entry_decision_receipts WHERE decision_id=$2) AND t.next_entry_position=32 AND t.catalog_revision=1 FROM content.entry_submissions s JOIN content.entry_review_requests r ON r.id=s.review_id JOIN content.reviewed_tasks t ON t.task_id=s.task_id WHERE s.id=$1`, sub.Id, decision.DecisionId)
	clear()
	armed.Store(true)
	_, err = faulty.ApplyEntryReviewDecision(serviceContext("moderation"), decision)
	entryCode(t, err, codes.Unavailable)
	var approved time.Time
	entryMust(t, l.owner.QueryRow(t.Context(), `SELECT approved_at FROM content.reviewed_entries WHERE id=$1`, sub.Id).Scan(&approved))
	var wg sync.WaitGroup
	failures := make(chan error, 6)
	for range 6 {
		wg.Go(func() { _, e := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), decision); failures <- e })
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		entryMust(t, e)
	}
	entryBool(t, l.owner, `SELECT e.position=32 AND e.approved_at=$2 AND s.approved_at=$2 AND s.state='approved' AND t.next_entry_position=33 AND t.catalog_revision=2 AND (SELECT count(*)=1 FROM content.entry_decision_receipts WHERE review_id=$3) FROM content.reviewed_entries e JOIN content.entry_submissions s ON s.id=e.id JOIN content.reviewed_tasks t ON t.task_id=e.task_id WHERE e.id=$1`, sub.Id, approved, decision.ReviewId)
	conflict := proto.Clone(decision).(*pb.ApplyEntryReviewDecisionRequest)
	conflict.Reason = proto.String("changed request")
	_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), conflict)
	entryCode(t, err, codes.AlreadyExists)
}

// Observe real PostgreSQL lock contention rather than guessing scheduling from
// a sleep. All waiters here use the random runtime role in this isolated DB.
func entryHoldParent(t *testing.T, l *entryLab, task string) (pgx.Tx, int32) {
	t.Helper()
	tx, err := l.owner.Begin(t.Context())
	entryMust(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	})
	var pid int32
	entryMust(t, tx.QueryRow(t.Context(), `SELECT pg_backend_pid() FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE`, task).Scan(&pid))
	return tx, pid
}

func entryWaitBlocked(t *testing.T, l *entryLab, blocker int32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		entryMust(t, l.admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE usename=$1 AND wait_event_type='Lock' AND $2=ANY(pg_blocking_pids(pid)))`, l.runtime.Config().ConnConfig.User, blocker).Scan(&waiting))
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("runtime request never reached the held parent lock")
		case <-tick.C:
		}
	}
}

// Pause only the first completed immutable-version query, after PostgreSQL has
// determined that rev2 does not exist. A second real connection can then commit
// rev2 before this request reads the current submission. No query/result is faked.
type entryVersionReadBarrier struct {
	ready, resume chan struct{}
	once          atomic.Bool
}

type entryVersionReadKey struct{}

func (b *entryVersionReadBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(d.SQL, "SELECT r.snapshot,r.content_hash,r.state,r.reason") {
		return context.WithValue(ctx, entryVersionReadKey{}, true)
	}
	return ctx
}

func (b *entryVersionReadBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if ctx.Value(entryVersionReadKey{}) == true && b.once.CompareAndSwap(false, true) {
		close(b.ready)
		select {
		case <-b.resume:
		case <-ctx.Done():
		}
	}
}

func TestEntryIntegrationResubmitBetweenVersionAndCurrentRead(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_content_%t", same), func(t *testing.T) {
			sub := entrySubmit(t, l.s, task, fmt.Sprintf("read-race-create-%t", same), entryTestInput(t, entryTestHuman))
			_, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), entryDecision(t, l, sub, "read-race-reject-"+sub.Id, false))
			entryMust(t, err)
			input := entryTestInput(t, entryTestHuman)
			input.Title = "committed revision two"
			winner := &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: input}
			lagging := proto.Clone(winner).(*pb.ResubmitRejectedEntryRequest)
			if !same {
				lagging.Content.Title = "different revision two"
			}
			barrier := &entryVersionReadBarrier{ready: make(chan struct{}), resume: make(chan struct{})}
			cfg := l.runtime.Config()
			cfg.ConnConfig.Tracer = barrier
			pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
			entryMust(t, err)
			t.Cleanup(pool.Close)
			slow := &Server{DB: pool, Identity: l.s.Identity}
			ctx, cancel := context.WithTimeout(entryServiceContext(t.Context(), "gateway"), 5*time.Second)
			defer cancel()
			type result struct {
				out *pb.ResubmitRejectedEntryResponse
				err error
			}
			results := make(chan result, 1)
			go func() { out, e := slow.ResubmitRejectedEntry(ctx, lagging); results <- result{out, e} }()
			select {
			case <-barrier.ready:
			case <-ctx.Done():
				t.Fatal("initial version lookup did not reach barrier")
			}
			accepted, err := l.s.ResubmitRejectedEntry(ctx, winner)
			close(barrier.resume)
			entryMust(t, err)
			got := <-results
			entryBool(t, l.owner, `SELECT revision=2 AND (SELECT count(*)=2 FROM content.entry_review_requests WHERE entry_id=$1) AND (SELECT count(*)=2 FROM content.entry_review_outbox o JOIN content.entry_review_requests r ON r.id=o.id WHERE r.entry_id=$1) FROM content.entry_submissions WHERE id=$1`, sub.Id)
			if same {
				entryMust(t, got.err)
				if !proto.Equal(accepted, got.out) {
					t.Fatal("same operation did not recover the committed immutable revision")
				}
			} else {
				entryCode(t, got.err, codes.AlreadyExists)
			}
		})
	}
}

func TestEntryIntegrationConcurrentResubmissions(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprintf("same_content_%t", same), func(t *testing.T) {
			sub := entrySubmit(t, l.s, task, fmt.Sprintf("concurrent-create-%t", same), entryTestInput(t, entryTestHuman))
			_, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), entryDecision(t, l, sub, "concurrent-reject-"+sub.Id, false))
			entryMust(t, err)
			const workers = 6
			requests := make([]*pb.ResubmitRejectedEntryRequest, workers)
			for i := range requests {
				input := entryTestInput(t, entryTestHuman)
				input.Title = "revised"
				if !same {
					input.Title = fmt.Sprintf("revision from worker %d", i)
				}
				requests[i] = &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: input}
			}
			lock, pid := entryHoldParent(t, l, task)
			type result struct {
				sub *pb.EntrySubmission
				err error
			}
			results := make(chan result, workers)
			ctx, cancel := context.WithTimeout(entryServiceContext(t.Context(), "gateway"), 10*time.Second)
			defer cancel()
			for _, r := range requests {
				go func() {
					out, e := l.s.ResubmitRejectedEntry(ctx, r)
					results <- result{out.GetSubmission(), e}
				}()
			}
			entryWaitBlocked(t, l, pid)
			entryMust(t, lock.Rollback(t.Context()))
			var accepted *pb.EntrySubmission
			success, conflicts := 0, 0
			for range workers {
				r := <-results
				if r.err != nil {
					entryCode(t, r.err, codes.AlreadyExists)
					conflicts++
					continue
				}
				success++
				if r.sub.GetRevision() != 2 || r.sub.GetState() != "pending_review" {
					t.Fatal("concurrent resubmission created wrong revision/state")
				}
				if accepted != nil && !proto.Equal(accepted, r.sub) {
					t.Fatal("same-content retries did not return the same immutable revision")
				}
				accepted = r.sub
			}
			if (same && (success != workers || conflicts != 0)) || (!same && (success != 1 || conflicts != workers-1)) {
				t.Fatalf("success=%d conflicts=%d same=%t", success, conflicts, same)
			}
			entryBool(t, l.owner, `SELECT s.revision=2 AND s.review_id=$2 AND s.content->>'title'=$3 AND (SELECT count(*)=2 FROM content.entry_review_requests WHERE entry_id=s.id) AND (SELECT count(*)=2 FROM content.entry_review_outbox o JOIN content.entry_review_requests r ON r.id=o.id WHERE r.entry_id=s.id) FROM content.entry_submissions s WHERE s.id=$1`, sub.Id, accepted.ReviewId, accepted.Content.Title)
			original := entrySubmit(t, l.s, task, fmt.Sprintf("concurrent-create-%t", same), entryTestInput(t, entryTestHuman))
			if original.Revision != 1 || original.State != "rejected" {
				t.Fatal("creation idempotency lost original revision after concurrent resubmit")
			}
		})
	}
}

func TestEntryIntegrationConcurrentApprovalPositions(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	const count = 12
	decisions := make([]*pb.ApplyEntryReviewDecisionRequest, count)
	for i := range decisions {
		input := entryTestInput(t, entryTestHuman)
		input.Title = fmt.Sprintf("work %d", i)
		sub := entrySubmit(t, l.s, task, fmt.Sprintf("parallel-create-%016d", i), input)
		decisions[i] = entryDecision(t, l, sub, fmt.Sprintf("parallel-approve-%016d", i), true)
	}
	lock, pid := entryHoldParent(t, l, task)
	ctx, cancel := context.WithTimeout(entryServiceContext(t.Context(), "moderation"), 10*time.Second)
	defer cancel()
	results := make(chan error, count)
	for _, decision := range decisions {
		go func() { _, e := l.s.ApplyEntryReviewDecision(ctx, decision); results <- e }()
	}
	entryWaitBlocked(t, l, pid)
	entryMust(t, lock.Rollback(t.Context()))
	for range count {
		entryMust(t, <-results)
	}
	entryBool(t, l.owner, `SELECT count(*)=$2 AND count(DISTINCT position)=$2 AND min(position)=32 AND max(position)=31+$2 AND bool_and(e.state='approved' AND e.approved_at IS NOT NULL AND s.approved_at=e.approved_at AND s.state='approved') FROM content.reviewed_entries e JOIN content.entry_submissions s ON s.id=e.id WHERE e.task_id=$1`, task, count)
	entryBool(t, l.owner, `SELECT next_entry_position=32+$2 AND catalog_revision=1+$2 AND revision=7 AND source_revision=3 AND published_at='2026-01-02' AND jsonb_array_length(snapshot->'entries')=1 FROM content.reviewed_tasks WHERE task_id=$1`, task, count)
	const catalog = `SELECT jsonb_agg(jsonb_build_array(id,position,approved_at,content) ORDER BY position) FROM content.reviewed_entries WHERE task_id=$1`
	var before, after []byte
	entryMust(t, l.owner.QueryRow(t.Context(), catalog, task).Scan(&before))
	for _, decision := range decisions {
		go func() { _, e := l.s.ApplyEntryReviewDecision(ctx, decision); results <- e }()
	}
	for range count {
		entryMust(t, <-results)
	}
	entryMust(t, l.owner.QueryRow(t.Context(), catalog, task).Scan(&after))
	if string(before) != string(after) {
		t.Fatal("decision replay moved positions, content or approval timestamps")
	}
	entryBool(t, l.owner, `SELECT count(*)=$2 FROM content.entry_decision_receipts d JOIN content.entry_review_requests r ON r.id=d.review_id JOIN content.entry_submissions s ON s.id=r.entry_id WHERE s.task_id=$1`, task, count)
}

func TestEntryIntegrationTakedownLockRaces(t *testing.T) {
	l := newEntryLab(t, false)
	for i, operation := range []string{"create", "resubmit", "approve", "reject"} {
		t.Run(operation, func(t *testing.T) {
			task := entrySeedParent(t, l, i+1)
			input := entryTestInput(t, entryTestHuman)
			var sub *pb.EntrySubmission
			if operation != "create" {
				sub = entrySubmit(t, l.s, task, "takedown-create-0001", input)
			}
			if operation == "resubmit" {
				_, e := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), entryDecision(t, l, sub, "takedown-old-reject-"+sub.Id, false))
				entryMust(t, e)
			}
			var decision *pb.ApplyEntryReviewDecisionRequest
			if operation == "approve" || operation == "reject" {
				decision = entryDecision(t, l, sub, "takedown-decision-"+sub.Id, operation == "approve")
			}
			lock, pid := entryHoldParent(t, l, task)
			// Takedown owns the exact parent row before the entry operation gets
			// there. The change is invisible to its initial reads until COMMIT.
			_, err := lock.Exec(t.Context(), `UPDATE content.reviewed_tasks SET state='taken_down',revision=revision+1 WHERE task_id=$1`, task)
			entryMust(t, err)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			result := make(chan error, 1)
			go func() {
				var e error
				switch operation {
				case "create":
					_, e = l.s.SubmitAdditionalEntry(entryServiceContext(ctx, "gateway"), &pb.SubmitAdditionalEntryRequest{ActorAssertion: "alice", TaskId: task, IdempotencyKey: "takedown-create-0001", Content: input})
				case "resubmit":
					_, e = l.s.ResubmitRejectedEntry(entryServiceContext(ctx, "gateway"), &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: input})
				default:
					_, e = l.s.ApplyEntryReviewDecision(entryServiceContext(ctx, "moderation"), decision)
				}
				result <- e
			}()
			entryWaitBlocked(t, l, pid)
			entryMust(t, lock.Commit(t.Context()))
			if operation == "reject" {
				entryMust(t, <-result)
			} else {
				entryCode(t, <-result, codes.FailedPrecondition)
			}
			entryBool(t, l.owner, `SELECT state='taken_down' AND next_entry_position=32 AND catalog_revision=1 AND (SELECT count(*)=1 FROM content.reviewed_entries WHERE task_id=$1) FROM content.reviewed_tasks WHERE task_id=$1`, task)
			if operation == "create" {
				entryBool(t, l.owner, `SELECT count(*)=0 FROM content.entry_submissions WHERE task_id=$1`, task)
			} else {
				state := "pending_review"
				if operation == "resubmit" || operation == "reject" {
					state = "rejected"
				}
				entryBool(t, l.owner, `SELECT s.state=$2 AND s.revision=1 AND s.approved_at IS NULL AND r.state=$2 AND (SELECT count(*)=1 FROM content.entry_review_requests WHERE entry_id=s.id) AND (SELECT count(*)=1 FROM content.entry_review_outbox o JOIN content.entry_review_requests er ON er.id=o.id WHERE er.entry_id=s.id) FROM content.entry_submissions s JOIN content.entry_review_requests r ON r.id=s.review_id WHERE s.id=$1`, sub.Id, state)
				if decision != nil {
					entryBool(t, l.owner, `SELECT count(*)=$2 FROM content.entry_decision_receipts WHERE decision_id=$1`, decision.DecisionId, map[bool]int{true: 1, false: 0}[operation == "reject"])
				}
				if state == "rejected" {
					out, e := l.s.GetMyEntrySubmission(serviceContext("gateway"), &pb.GetMyEntrySubmissionRequest{ActorAssertion: "alice", EntryId: sub.Id})
					entryMust(t, e)
					if out.Submission.GetRejectionReason() != "please revise" {
						t.Fatal("takedown hid durable rejection feedback")
					}
				}
			}
		})
	}
}

func TestEntryIntegrationStaleDecisionsCannotRestore(t *testing.T) {
	l := newEntryLab(t, false)
	task := entrySeedParent(t, l, 1)
	sub := entrySubmit(t, l.s, task, "stale-create-00001", entryTestInput(t, entryTestHuman))
	old := entryDecision(t, l, sub, "stale-first-reject-01", false)
	oldReceipt, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), old)
	entryMust(t, err)
	input := entryTestInput(t, entryTestHuman)
	input.Title = "second revision"
	newer, err := l.s.ResubmitRejectedEntry(serviceContext("gateway"), &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: input})
	entryMust(t, err)
	replay, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), old)
	entryMust(t, err)
	if !proto.Equal(oldReceipt, replay) {
		t.Fatal("old decision receipt changed")
	}
	stale := proto.Clone(old).(*pb.ApplyEntryReviewDecisionRequest)
	stale.DecisionId = "stale-new-decision-01"
	stale.Outcome = pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE
	_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), stale)
	entryCode(t, err, codes.Aborted)
	entryBool(t, l.owner, `SELECT revision=2 AND state='pending_review' AND content->>'title'='second revision' AND rejection_reason IS NULL FROM content.entry_submissions WHERE id=$1`, sub.Id)
	approve := entryDecision(t, l, newer.Submission, "stale-second-approve-01", true)
	approved, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), approve)
	entryMust(t, err)
	_, err = l.s.ApplyAssetReview(serviceContext("moderation"), &pb.ApplyAssetReviewRequest{DecisionId: "stale-revoke-000001", TaskId: task, SubmissionRevision: 3, ExpectedRevision: 7, Decision: &pb.ApplyAssetReviewRequest_EntryReview{EntryReview: &pb.EntryAssetReview{EntryId: sub.Id, State: pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVAL_REVOKED}}})
	entryMust(t, err)
	for _, r := range []*pb.ApplyEntryReviewDecisionRequest{old, approve} {
		_, err = l.s.ApplyEntryReviewDecision(serviceContext("moderation"), r)
		entryMust(t, err)
	}
	entryBool(t, l.owner, `SELECT e.state='approval_revoked' AND s.state='approval_revoked' AND s.revision=2 AND e.position=32 AND t.next_entry_position=33 AND t.catalog_revision=3 AND t.revision=8 FROM content.reviewed_entries e JOIN content.entry_submissions s ON s.id=e.id JOIN content.reviewed_tasks t ON t.task_id=e.task_id WHERE e.id=$1`, sub.Id)
	_, err = l.s.ApplyAssetReview(serviceContext("moderation"), &pb.ApplyAssetReviewRequest{DecisionId: "stale-takedown-0001", TaskId: task, SubmissionRevision: 3, ExpectedRevision: 8, Decision: &pb.ApplyAssetReviewRequest_TaskState{TaskState: pb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN}})
	entryMust(t, err)
	again, err := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), approve)
	entryMust(t, err)
	if !proto.Equal(approved, again) {
		t.Fatal("approved receipt was rewritten after tightening")
	}
	entryBool(t, l.owner, `SELECT state='taken_down' AND revision=9 AND catalog_revision=4 AND next_entry_position=33 FROM content.reviewed_tasks WHERE task_id=$1`, task)
	_, err = l.s.ListApprovedEntries(serviceContext("gateway"), &pb.ListApprovedEntriesRequest{ActorAssertion: "alice", TaskId: task})
	entryCode(t, err, codes.NotFound)
}

func TestEntryIntegrationCanceledWaitersReleaseLocksAndPool(t *testing.T) {
	l := newEntryLab(t, false)
	// With one connection, a leaked checked-out connection cannot be hidden by
	// a successful Ping on a spare connection.
	cfg := l.runtime.Config()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	entryMust(t, err)
	t.Cleanup(pool.Close)
	s := &Server{DB: pool, Identity: l.s.Identity}
	for i, operation := range []string{"create", "resubmit", "approve"} {
		for j, mode := range []string{"cancel", "deadline"} {
			t.Run(operation+"/"+mode, func(t *testing.T) {
				task := entrySeedParent(t, l, 10+i*2+j)
				input := entryTestInput(t, entryTestHuman)
				var sub *pb.EntrySubmission
				if operation != "create" {
					sub = entrySubmit(t, l.s, task, "canceled-create-0001", input)
				}
				if operation == "resubmit" {
					_, e := l.s.ApplyEntryReviewDecision(serviceContext("moderation"), entryDecision(t, l, sub, "canceled-reject-"+sub.Id, false))
					entryMust(t, e)
				}
				var decision *pb.ApplyEntryReviewDecisionRequest
				if operation == "approve" {
					decision = entryDecision(t, l, sub, "canceled-approve-"+sub.Id, true)
				}
				call := func(ctx context.Context) error {
					switch operation {
					case "create":
						_, e := s.SubmitAdditionalEntry(entryServiceContext(ctx, "gateway"), &pb.SubmitAdditionalEntryRequest{ActorAssertion: "alice", TaskId: task, IdempotencyKey: "canceled-create-0001", Content: input})
						return e
					case "resubmit":
						_, e := s.ResubmitRejectedEntry(entryServiceContext(ctx, "gateway"), &pb.ResubmitRejectedEntryRequest{ActorAssertion: "alice", EntryId: sub.Id, ExpectedRevision: 1, Content: input})
						return e
					default:
						_, e := s.ApplyEntryReviewDecision(entryServiceContext(ctx, "moderation"), decision)
						return e
					}
				}
				lock, pid := entryHoldParent(t, l, task)
				deadline := 10 * time.Second
				if mode == "deadline" {
					deadline = 2 * time.Second
				}
				ctx, cancel := context.WithTimeout(t.Context(), deadline)
				defer cancel()
				result := make(chan error, 1)
				go func() { result <- call(ctx) }()
				entryWaitBlocked(t, l, pid)
				if mode == "cancel" {
					cancel()
				}
				select {
				case e := <-result:
					want := codes.Canceled
					if mode == "deadline" {
						want = codes.DeadlineExceeded
					}
					entryCode(t, e, want)
				case <-time.After(4 * time.Second):
					t.Fatal("canceled database lock waiter did not return")
				}
				check, stop := context.WithTimeout(t.Context(), 2*time.Second)
				defer stop()
				entryMust(t, pool.Ping(check))
				if pool.Stat().AcquiredConns() != 0 {
					t.Fatal("runtime connection remained checked out")
				}
				entryBool(t, l.admin, `SELECT NOT EXISTS(SELECT 1 FROM pg_stat_activity WHERE usename=$1 AND xact_start IS NOT NULL)`, cfg.ConnConfig.User)
				if decision != nil {
					probe, e := l.owner.Begin(check)
					entryMust(t, e)
					var unlocked bool
					e = probe.QueryRow(check, `SELECT pg_try_advisory_xact_lock(hashtextextended($1,7185429007))`, decision.DecisionId).Scan(&unlocked)
					_ = probe.Rollback(check)
					entryMust(t, e)
					if !unlocked {
						t.Fatal("canceled approval retained decision advisory lock")
					}
				}
				entryMust(t, lock.Rollback(check))
				entryBool(t, l.owner, `SELECT next_entry_position=32 AND catalog_revision=1 AND (SELECT count(*)=1 FROM content.reviewed_entries WHERE task_id=$1) FROM content.reviewed_tasks WHERE task_id=$1`, task)
				entryMust(t, call(check))
			})
		}
	}
}
