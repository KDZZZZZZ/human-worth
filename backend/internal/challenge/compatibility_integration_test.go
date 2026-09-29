//go:build integration

package challenge_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/repo/postgres"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Fixtures use the pre-refactor protobuf and hashing, independently of the new mapper.
func TestLegacyRunAndTransactionalRollback(t *testing.T) {
	l := newLab(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	score := .9
	run := &pb.Run{Id: "run_legacy", TaskId: "target", AdministratorId: "account_admin", Status: "completed", Stage: "finished", Round: 2, RankerRound: 1, Configuration: config(), RankerFit: &pb.RankerFit{Status: "passed", Score: &score, EvaluatedAt: timestamppb.New(now)}, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now), EndedAt: timestamppb.New(now), Candidates: []*pb.Candidate{{Id: "work_legacy", RegistrationState: "registered", EntryId: "entry_legacy", ReviewId: "review_legacy"}}}
	legacy, err := proto.Marshal(&pb.StoredRun{Run: run, RankerPrompt: "已有的合格排序规则", ExecutionHints: "已有的执行提示", TerminalTarget: "completed", ReservedCalls: 9})
	must(t, err)
	configBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(run.Configuration)
	must(t, err)
	hash := func(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
	requestHash := hash([]byte("target\n\n" + hash(configBytes)))
	_, err = l.db.Exec(t.Context(), `INSERT INTO challenge.runs(id,task_id,administrator_id,operation,idempotency_key,request_hash,status,pending,deadline,state) VALUES($1,'target','account_admin','start','legacy_key',$2,'completed','',$3,$4)`, run.Id, requestHash, now.Add(time.Hour), legacy)
	must(t, err)
	if got := l.get(run.Id); !proto.Equal(got, run) {
		t.Fatal("existing stored run changed across the layer boundary")
	}
	// A replay must be resolved before dependency preparation, even if materials are now unavailable.
	l.deps.mu.Lock()
	l.deps.allowed = false
	l.deps.mu.Unlock()
	replayed, err := l.admins[1].StartRun(t.Context(), &pb.StartRunRequest{ActorAssertion: "admin", TaskId: "target", Configuration: run.Configuration, IdempotencyKey: "legacy_key"})
	must(t, err)
	if !proto.Equal(replayed.Run, run) {
		t.Fatal("legacy idempotency key created or returned a different run")
	}
	changed := proto.Clone(run.Configuration).(*pb.RunConfiguration)
	changed.Packer.Prompt = "different request"
	_, err = l.admins[1].StartRun(t.Context(), &pb.StartRunRequest{ActorAssertion: "admin", TaskId: "target", Configuration: changed, IdempotencyKey: "legacy_key"})
	if status.Code(err) != codes.Aborted {
		t.Fatalf("changed legacy request: %v", err)
	}

	store := postgres.New(l.db)
	abort := errors.New("abort transaction after state and audit writes")
	err = store.WithinTx(t.Context(), func(tx application.Tx) error {
		r, err := tx.Load(t.Context(), run.Id)
		if err != nil {
			return err
		}
		r.Run.FailureReason = "must roll back"
		if err = tx.Save(t.Context(), r); err != nil {
			return err
		}
		if err = tx.Audit(t.Context(), run.Id, "operator", "rollback_probe", ""); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("transaction error lost: %v", err)
	}
	var after []byte
	var version, audits int64
	must(t, l.db.QueryRow(t.Context(), `SELECT state,version,(SELECT count(*) FROM challenge.audit_events WHERE run_id=$1) FROM challenge.runs WHERE id=$1`, run.Id).Scan(&after, &version, &audits))
	if !bytes.Equal(after, legacy) || version != 1 || audits != 0 {
		t.Fatal("state/version and audit did not roll back together")
	}
	err = store.WithinTx(t.Context(), func(tx application.Tx) error {
		r, err := tx.Load(t.Context(), run.Id)
		if err != nil {
			return err
		}
		r.Run.FailureReason = "committed"
		if err = tx.Save(t.Context(), r); err != nil {
			return err
		}
		return tx.Audit(t.Context(), run.Id, "operator", "commit_probe", "")
	})
	must(t, err)
	if l.get(run.Id).FailureReason != "committed" {
		t.Fatal("committed state unavailable through gRPC")
	}
	must(t, l.db.QueryRow(t.Context(), `SELECT version,(SELECT count(*) FROM challenge.audit_events WHERE run_id=$1) FROM challenge.runs WHERE id=$1`, run.Id).Scan(&version, &audits))
	if version != 2 || audits != 1 {
		t.Fatal("state and audit were not committed together")
	}
}
