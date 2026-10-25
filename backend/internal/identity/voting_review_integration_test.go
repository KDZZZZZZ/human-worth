//go:build integration

package identity

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/KDZZZZZZ/human-worth/backend/internal/voting"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Run the real entrypoint: the shared in-process fixture installs its own stream
// interceptor and would mask a missing interceptor in cmd/voting.
func TestVotingProcessHealthStreamAuthorization(t *testing.T) {
	l := newIdentityLab(t)
	db := serviceTestDatabase(t, l, "voting", voting.Migrate, "../../../ops/voting/grants.sql")
	dir := t.TempDir()
	binary := filepath.Join(dir, "voting")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/voting")
	build.Dir = "../.."
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Voting: %v\n%s", err, raw)
	}
	cc := db.Config().ConnConfig
	dsn := &url.URL{Scheme: "postgres", User: url.UserPassword(cc.User, cc.Password), Host: net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))), Path: "/" + cc.Database, RawQuery: "sslmode=disable"}
	secret := filepath.Join(dir, "database-url")
	must(t, os.WriteFile(secret, []byte(dsn.String()), 0600))
	cert, key, ca := l.pki.cert(t, "voting")
	address := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		defer listener.Close()
		return listener.Addr().String()
	}
	rpcAddress, healthAddress := address(), address()
	process := exec.CommandContext(t.Context(), binary)
	process.Env = append(os.Environ(), "VOTING_DATABASE_URL_FILE="+secret, "SERVICE_CERT_FILE="+cert, "SERVICE_KEY_FILE="+key, "SERVICE_CA_FILE="+ca, "IDENTITY_TARGET=127.0.0.1:1", "CONTENT_TARGET=127.0.0.1:1", "GRPC_LISTEN="+rpcAddress, "HEALTH_LISTEN="+healthAddress)
	log, err := os.Create(filepath.Join(dir, "process.log"))
	must(t, err)
	process.Stdout, process.Stderr = log, log
	must(t, process.Start())
	t.Cleanup(func() { process.Process.Kill(); process.Wait(); log.Close() })
	client := func(caller string) healthpb.HealthClient {
		c, k, a := l.pki.cert(t, caller)
		var config *tls.Config
		config, err = platform.TLS(c, k, a, "voting")
		must(t, err)
		conn, err := grpc.NewClient(rpcAddress, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithDisableRetry())
		must(t, err)
		t.Cleanup(func() { conn.Close() })
		return healthpb.NewHealthClient(conn)
	}
	gateway := client("gateway")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, err = gateway.Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	must(t, err)
	for _, caller := range []string{"asset", "content", "gateway"} {
		t.Run(caller, func(t *testing.T) {
			c := client(caller)
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, checkErr := c.Check(ctx, &healthpb.HealthCheckRequest{})
			if caller == "asset" {
				grpcCode(t, checkErr, codes.PermissionDenied)
			} else {
				must(t, checkErr)
			}
			stream, watchErr := c.Watch(ctx, &healthpb.HealthCheckRequest{})
			if watchErr == nil {
				_, watchErr = stream.Recv()
			}
			want := codes.PermissionDenied
			if caller == "gateway" {
				want = codes.Unimplemented // no supported streaming RPC in this stage
			}
			grpcCode(t, watchErr, want)
		})
	}
}

type votingCommitBarrier struct {
	once             sync.Once
	reached, release chan struct{}
}

func (b *votingCommitBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if d.SQL == "commit" {
		b.once.Do(func() {
			close(b.reached)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*votingCommitBarrier) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// Fix both actual PostgreSQL commit orders, not merely two concurrent goroutines.
// The second RPC must wait on the same account/task guard until the first commits.
func TestVotingCastAndStatisticsCommitOrders(t *testing.T) {
	for _, first := range []string{"vote", "statistics"} {
		t.Run(first+"-first", func(t *testing.T) {
			f := newAdditionalEntryLab(t, true)
			task := f.publish(t, "voting-commit-order-task")
			ballot := votingBallotFor(t, f, f.bob, task.ID)
			barrier := &votingCommitBarrier{reached: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(barrier.release) }) }
			defer unblock()
			pool := func(tracer pgx.QueryTracer, name string) *pgxpool.Pool {
				cfg := f.votingDB.Config()
				cfg.ConnConfig.Tracer = tracer
				cfg.ConnConfig.RuntimeParams["application_name"] = name
				p, err := pgxpool.NewWithConfig(t.Context(), cfg)
				must(t, err)
				t.Cleanup(p.Close)
				return p
			}
			f.votes[0].DB = pool(barrier, "voting-ordered-first")
			f.votes[1].DB = pool(nil, "voting-ordered-second")
			req := &votingpb.CastVoteRequest{ActorAssertion: votingAssertion(t, f, f.bob, votingpb.VotingService_CastVote_FullMethodName), TaskId: task.ID, BallotId: ballot.ID, EntryId: ballot.Entries[0].ID}
			stat := &votingpb.ViewTaskStatisticsRequest{ActorAssertion: votingAssertion(t, f, f.bob, votingpb.VotingService_ViewTaskStatistics_FullMethodName), TaskId: task.ID, PermanentlyCloseVoting: true}
			type result struct {
				kind  string
				err   error
				total int64
			}
			done := make(chan result, 2)
			call := func(index int, kind string) {
				if kind == "vote" {
					_, err := f.voteClients[index].CastVote(t.Context(), req)
					done <- result{kind: kind, err: err}
				} else {
					out, err := f.voteClients[index].ViewTaskStatistics(t.Context(), stat)
					done <- result{kind: kind, err: err, total: out.GetStatistics().GetTotalVotes()}
				}
			}
			go call(0, first)
			select {
			case <-barrier.reached:
			case <-time.After(5 * time.Second):
				t.Fatal("first RPC did not reach COMMIT")
			}
			second := "statistics"
			if first == "statistics" {
				second = "vote"
			}
			go call(1, second)
			deadline := time.Now().Add(time.Second)
			// CastVote now holds the exclusive task gate while it atomically
			// updates conclusion history. Both commit orders therefore wait at
			// this outer gate before reaching the unchanged account/task guard.
			for {
				var waiting bool
				must(t, f.identity.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND application_name='voting-ordered-second' AND wait_event_type='Lock' AND query LIKE '%voting.catalog_gates%')`).Scan(&waiting))
				if waiting {
					break
				}
				select {
				case r := <-done:
					t.Fatalf("second RPC escaped the task serialization gate: %+v", r)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("second RPC did not wait on the task serialization gate")
				}
				time.Sleep(5 * time.Millisecond)
			}
			unblock()
			wantCount := int64(0)
			if first == "vote" {
				wantCount = 1
			}
			for range 2 {
				r := <-done
				if r.kind == "vote" && first == "statistics" {
					grpcCode(t, r.err, codes.PermissionDenied)
				} else {
					must(t, r.err)
				}
				if r.kind == "statistics" && r.total != wantCount {
					t.Fatal("statistics and committed vote order diverged", r.total, wantCount)
				}
			}
			var n int64
			must(t, f.votingDB.QueryRow(t.Context(), `SELECT count(*) FROM voting.current_votes WHERE task_id=$1`, task.ID).Scan(&n))
			if n != wantCount {
				t.Fatal(fmt.Sprint("unexpected durable vote count: ", n))
			}
			_, err := f.voteClients[1].CastVote(t.Context(), req)
			if status.Code(err) != codes.PermissionDenied {
				t.Fatal("permanent closure failed to reject replay", err)
			}
		})
	}
}

func TestVotingBallotExpiryAndBoundedCleanup(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-expiry-task")
	ballot := votingBallotFor(t, f, f.bob, task.ID)
	base := "/api/tasks/" + task.ID
	body := votingBody(ballot, ballot.Entries[0].ID, 0)
	var saved, replay votingVote
	votingRead(t, f.request(t, 0, f.bob, "PUT", base+"/vote", body, nil, 200), &saved)
	_, err := f.identity.db.Exec(t.Context(), `UPDATE voting.ballots SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, ballot.ID)
	must(t, err)
	votingRead(t, f.request(t, 1, f.bob, "PUT", base+"/vote", body, nil, 200), &replay)
	if replay != saved {
		t.Fatal("expiry changed exact most recent receipt", saved, replay)
	}
	f.request(t, 0, f.bob, "PUT", base+"/vote", votingBody(ballot, ballot.Entries[1].ID, 1), nil, 409).Body.Close()
	_, err = f.identity.db.Exec(t.Context(), `UPDATE voting.ballots SET expires_at=clock_timestamp()-interval '2 days' WHERE id=$1;
INSERT INTO voting.ballots(id,account_id,task_id,generation,sha256,expires_at)
SELECT 'bal_expired_'||n,$2,$3,1,'obsolete',clock_timestamp()-interval '3 days' FROM generate_series(1,300)n`, pgx.QueryExecModeSimpleProtocol, ballot.ID, f.bob.account, task.ID)
	must(t, err)
	must(t, f.votes[0].CleanupBallots(t.Context()))
	var count int
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT count(*) FROM voting.ballots WHERE task_id=$1`, task.ID).Scan(&count))
	if count != 45 {
		t.Fatal("cleanup was not bounded at 256", count)
	}
	must(t, f.votes[1].CleanupBallots(t.Context()))
	f.request(t, 1, f.bob, "PUT", base+"/vote", body, nil, 409).Body.Close()
	f.request(t, 1, f.bob, "POST", base+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200).Body.Close()
	f.request(t, 0, f.bob, "PUT", base+"/vote", body, nil, 403).Body.Close()
	must(t, f.votes[0].CleanupBallots(t.Context()))
	var votes, closures int
	must(t, f.votingDB.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM voting.current_votes WHERE task_id=$1),(SELECT count(*) FROM voting.statistics_access WHERE task_id=$1)`, task.ID).Scan(&votes, &closures))
	if votes != 1 || closures != 1 {
		t.Fatal("ballot cleanup removed persistent business facts", votes, closures)
	}
}

func TestVotingCatalogUnknownDatabaseCommit(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-protocol-unknown-task")
	ctx := t.Context()
	current, err := f.catalogs[0].GetVotingCatalogSnapshot(ctx, &contentpb.GetVotingCatalogSnapshotRequest{TaskId: task.ID})
	must(t, err)
	change := "chg_1234567890abcdef1234567890abcdef"
	prepare := &votingpb.PrepareCatalogChangeRequest{ChangeId: change, TaskId: task.ID, ExpectedGeneration: current.Catalog.Generation, Generation: current.Catalog.Generation + 1}
	f.votes[0].DB = votingNthCommitPool(t, f.votingDB, 1)
	_, err = f.contents[0].Voting.PrepareCatalogChange(ctx, prepare)
	if err == nil {
		t.Fatal("lost real prepare COMMIT was not observed")
	}
	prepared, err := f.contents[1].Voting.PrepareCatalogChange(ctx, prepare)
	must(t, err)
	if prepared.Transition.Phase != "prepared" {
		t.Fatal("prepare recovery lost frozen intent")
	}
	target := proto.Clone(current.Catalog).(*contentpb.VotingCatalogSnapshot)
	target.Generation++
	target.Sha256 = contract.CatalogHash(target)
	activate := &votingpb.ActivateCatalogSnapshotRequest{ChangeId: change, Catalog: target}
	f.votes[0].DB = votingNthCommitPool(t, f.votingDB, 1)
	_, err = f.contents[0].Voting.ActivateCatalogSnapshot(ctx, activate)
	if err == nil {
		t.Fatal("lost real activate COMMIT was not observed")
	}
	activated, err := f.contents[1].Voting.ActivateCatalogSnapshot(ctx, activate)
	must(t, err)
	if activated.Transition.Phase != "activated" || activated.Transition.Sha256 != target.Sha256 {
		t.Fatal("activate recovery lost exact receipt")
	}
	var changes int
	must(t, f.votingDB.QueryRow(ctx, `SELECT count(*) FROM voting.catalog_changes WHERE task_id=$1`, task.ID).Scan(&changes))
	if changes != 2 {
		t.Fatal("unknown commit duplicated protocol change", changes)
	}
}

func TestVotingCancelledActivationReleasesContentLocks(t *testing.T) {
	f := newAdditionalEntryLab(t, true)
	task := f.publish(t, "voting-cancel-activation-task")
	barrier := &votingActivationBarrier{VotingServiceClient: f.contents[1].Voting, activated: make(chan struct{}), release: make(chan struct{})}
	f.contents[1].Voting = barrier
	defer close(barrier.release)
	var source, revision int64
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT source_revision,revision FROM content.reviewed_tasks WHERE task_id=$1`, task.ID).Scan(&source, &revision))
	req := &contentpb.ApplyAssetReviewRequest{TaskId: task.ID, DecisionId: "voting-cancel-activation-down", SubmissionRevision: source, ExpectedRevision: revision, Decision: &contentpb.ApplyAssetReviewRequest_TaskState{TaskState: contentpb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.reviewers[1].ApplyAssetReview(ctx, req); done <- err }()
	select {
	case <-barrier.activated:
	case <-time.After(5 * time.Second):
		t.Fatal("activation did not reach external I/O barrier")
	}
	// The remote RPC has committed but its acknowledgement is held back. Local
	// Content head/business rows and the remote gate must already be unlocked.
	for _, sql := range []string{
		`SELECT task_id FROM content.voting_catalog_heads WHERE task_id=$1 FOR UPDATE NOWAIT`,
		`SELECT id FROM content.task_drafts WHERE id=$1 FOR UPDATE NOWAIT`,
		`SELECT task_id FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE NOWAIT`,
		`SELECT task_id FROM voting.catalog_gates WHERE task_id=$1 FOR UPDATE NOWAIT`,
	} {
		tx, err := f.identity.db.Begin(t.Context())
		must(t, err)
		_, err = tx.Exec(t.Context(), sql, task.ID)
		rollback := tx.Rollback(context.Background())
		must(t, err)
		must(t, rollback)
	}
	f.request(t, 0, additionalActor{}, "GET", "/api/tasks/"+task.ID, "", nil, 404).Body.Close()
	cancel()
	grpcCode(t, <-done, codes.Canceled)
	_, err := f.identity.db.Exec(t.Context(), `UPDATE content.voting_catalog_changes SET available_at=clock_timestamp() WHERE task_id=$1 AND stage='applied'`, task.ID)
	must(t, err)
	recovered, err := f.contents[0].DeliverCatalog(t.Context())
	must(t, err)
	if recovered.Outcome != "delivered" {
		t.Fatal("cancelled activation did not recover", recovered)
	}
	_, err = f.reviewers[0].ApplyAssetReview(t.Context(), req)
	must(t, err)
	var generation int64
	var active *string
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT generation,active_change FROM content.voting_catalog_heads WHERE task_id=$1`, task.ID).Scan(&generation, &active))
	if generation != 2 || active != nil {
		t.Fatal("cancellation corrupted durable transition", generation, active)
	}
}
