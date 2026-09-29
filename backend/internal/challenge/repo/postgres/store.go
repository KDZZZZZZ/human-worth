package postgres

import (
	"context"
	"embed"
	"errors"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/proto"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	return platform.Migrate(ctx, db, migrations, "challenge", 7185429002)
}
func Ready(ctx context.Context, db *pgxpool.Pool) error {
	var ok bool
	err := db.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM challenge.schema_migrations WHERE version='001_challenge.sql')").Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("challenge schema not ready")
	}
	return nil
}

type Store struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

type transaction struct{ tx pgx.Tx }

func persistence(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}
	return domain.UnavailableError()
}
func (s *Store) WithinTx(ctx context.Context, run func(application.Tx) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return persistence(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	if err = run(&transaction{tx: tx}); err != nil {
		return err
	}
	return persistence(tx.Commit(ctx))
}
func (t *transaction) Now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := t.tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now)
	return now, persistence(err)
}
func (t *transaction) load(ctx context.Context, run string, lock bool) (*application.Record, error) {
	query := "SELECT state,version,clock_timestamp(),deadline,worker_id FROM challenge.runs WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	r := &application.Record{}
	var data []byte
	if err := t.tx.QueryRow(ctx, query, run).Scan(&data, &r.Version, &r.Now, &r.Deadline, &r.Worker); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.Reject(domain.NotFound, "run_not_found")
		}
		return nil, persistence(err)
	}
	st := &pb.StoredRun{}
	if proto.Unmarshal(data, st) != nil {
		return nil, domain.UnavailableError()
	}
	r.State = protobuf.FromState(st)
	return r, nil
}
func (t *transaction) Load(ctx context.Context, id string) (*application.Record, error) {
	return t.load(ctx, id, true)
}
func (s *Store) Read(ctx context.Context, id string) (out *application.Record, err error) {
	err = s.WithinTx(ctx, func(tx application.Tx) error { out, err = tx.(*transaction).load(ctx, id, false); return err })
	return
}
func (t *transaction) Save(ctx context.Context, r *application.Record) error {
	r.Run.UpdatedAt = domain.TimePtr(r.Now)
	data, err := proto.Marshal(protobuf.ToState(r.State))
	if err != nil {
		return domain.UnavailableError()
	}
	var kind int32
	var lease *time.Time
	leased := false
	if a := r.Assignment; a != nil {
		kind = int32(a.Kind)
		if a.Attempt != nil {
			leased = true
			lease = a.LeaseExpiresAt
		}
	}
	if !leased {
		r.Worker = ""
	}
	_, err = t.tx.Exec(ctx, `UPDATE challenge.runs SET status=$2,pending=$3,kind=$4,leased=$5,lease_until=$6,state=$7,worker_id=$8,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, r.Run.Id, r.Run.Status, r.Pending, kind, leased, lease, data, r.Worker)
	return persistence(err)
}
func (s *Store) LookupRun(ctx context.Context, key application.RunKey) (*domain.Run, error) {
	var digest string
	var data []byte
	err := s.db.QueryRow(ctx, `SELECT request_hash,state FROM challenge.runs WHERE administrator_id=$1 AND operation=$2 AND idempotency_key=$3`, key.Administrator, key.Operation, key.Key).Scan(&digest, &data)
	if err != nil {
		return nil, persistence(err)
	}
	if digest != key.Hash {
		return nil, domain.Reject(domain.Aborted, "idempotency_conflict")
	}
	st := &pb.StoredRun{}
	if proto.Unmarshal(data, st) != nil {
		return nil, domain.UnavailableError()
	}
	return protobuf.FromRun(st.Run), nil
}
func (t *transaction) InsertRun(ctx context.Context, key application.RunKey, st *domain.State, deadline time.Time) (bool, error) {
	data, err := proto.Marshal(protobuf.ToState(st))
	if err != nil {
		return false, domain.UnavailableError()
	}
	result, err := t.tx.Exec(ctx, `INSERT INTO challenge.runs(id,task_id,administrator_id,parent_run_id,operation,idempotency_key,request_hash,status,pending,deadline,state) VALUES($1,$2,$3,$4,$5,$6,$7,'queued','open',$8,$9) ON CONFLICT(administrator_id,operation,idempotency_key) DO NOTHING`, st.Run.Id, st.Run.TaskId, key.Administrator, st.Run.ParentRunId, key.Operation, key.Key, key.Hash, deadline, data)
	return result.RowsAffected() != 0, persistence(err)
}
func (t *transaction) Audit(ctx context.Context, run, actor, action, reason string) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO challenge.audit_events(run_id,actor_id,action,reason) VALUES($1,$2,$3,$4)`, run, actor, action, reason)
	return persistence(err)
}
func (s *Store) ListRuns(ctx context.Context, status, cursor string, limit int32) ([]*domain.Run, error) {
	rows, err := s.db.Query(ctx, `SELECT state FROM challenge.runs WHERE ($1='' OR status=$1) AND ($2='' OR (created_at,id)<(SELECT created_at,id FROM challenge.runs WHERE id=$2)) ORDER BY created_at DESC,id DESC LIMIT $3`, status, cursor, limit)
	if err != nil {
		return nil, persistence(err)
	}
	defer rows.Close()
	var out []*domain.Run
	for rows.Next() {
		var data []byte
		if rows.Scan(&data) != nil {
			return nil, domain.UnavailableError()
		}
		st := &pb.StoredRun{}
		if proto.Unmarshal(data, st) != nil {
			return nil, domain.UnavailableError()
		}
		out = append(out, protobuf.FromRun(st.Run))
	}
	return out, persistence(rows.Err())
}
func (s *Store) Summary(ctx context.Context) (*dto.GetRunSummaryResponse, error) {
	out := &dto.GetRunSummaryResponse{}
	err := s.db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN ('queued','active','cancelling','finalizing')),count(*) FILTER(WHERE status='failed') FROM challenge.runs`).Scan(&out.ActiveRuns, &out.FailedRuns)
	return out, persistence(err)
}
func (s *Store) PendingRuns(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM challenge.runs WHERE status IN ('queued','active','cancelling','finalizing') ORDER BY updated_at,id LIMIT 50`)
	if err != nil {
		return nil, persistence(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, persistence(err)
		}
		out = append(out, id)
	}
	return out, persistence(rows.Err())
}
func (t *transaction) LockClaims(ctx context.Context) error {
	_, err := t.tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7185429003)")
	return persistence(err)
}
func (t *transaction) FindClaim(ctx context.Context, owner, request string) (application.ClaimReceipt, error) {
	var out application.ClaimReceipt
	err := t.tx.QueryRow(ctx, `SELECT run_id,attempt_id,capabilities_hash FROM challenge.claims WHERE worker_id=$1 AND request_id=$2`, owner, request).Scan(&out.RunID, &out.AttemptID, &out.Hash)
	return out, persistence(err)
}
func (t *transaction) LeaseUsage(ctx context.Context, owner string) (int, bool, error) {
	var count int
	var own bool
	err := t.tx.QueryRow(ctx, `SELECT count(*),COALESCE(bool_or(worker_id=$1),false) FROM challenge.runs WHERE leased AND status='active'`, owner).Scan(&count, &own)
	return count, own, persistence(err)
}
func (t *transaction) NextRun(ctx context.Context, kinds []int32) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx, `SELECT id FROM challenge.runs WHERE status='active' AND pending='' AND NOT leased AND kind=ANY($1) AND deadline>clock_timestamp() ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, kinds).Scan(&id)
	return id, persistence(err)
}
func (t *transaction) AddClaim(ctx context.Context, owner, request string, v application.ClaimReceipt) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO challenge.claims(worker_id,request_id,capabilities_hash,run_id,attempt_id) VALUES($1,$2,$3,$4,$5)`, owner, request, v.Hash, v.RunID, v.AttemptID)
	return persistence(err)
}
func (t *transaction) FindModelCall(ctx context.Context, attempt, request string) (application.ModelCall, error) {
	var v application.ModelCall
	err := t.tx.QueryRow(ctx, `SELECT id,state,request_digest FROM challenge.model_calls WHERE attempt_id=$1 AND request_id=$2`, attempt, request).Scan(&v.ID, &v.State, &v.Digest)
	return v, persistence(err)
}
func (t *transaction) ModelCallCounts(ctx context.Context, attempt string) (int, int, error) {
	var count, unknown int
	err := t.tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state IN ('reserved','unknown')) FROM challenge.model_calls WHERE attempt_id=$1`, attempt).Scan(&count, &unknown)
	return count, unknown, persistence(err)
}
func (t *transaction) AddModelCall(ctx context.Context, r *application.Record, q *dto.ReserveModelCallRequest, id string) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO challenge.model_calls(id,run_id,attempt_id,request_id,request_digest,worker_id,lease_epoch,state) VALUES($1,$2,$3,$4,$5,$6,$7,'reserved')`, id, r.Run.Id, q.Attempt.AttemptId, q.RequestId, q.RequestDigest, r.Worker, q.Attempt.LeaseEpoch)
	return persistence(err)
}
func (t *transaction) LockModelCall(ctx context.Context, q *dto.SettleModelCallRequest, owner string) (string, error) {
	var previous string
	err := t.tx.QueryRow(ctx, `SELECT state FROM challenge.model_calls WHERE id=$1 AND run_id=$2 AND attempt_id=$3 AND worker_id=$4 AND lease_epoch=$5 FOR UPDATE`, q.ReservationId, q.Attempt.RunId, q.Attempt.AttemptId, owner, q.Attempt.LeaseEpoch).Scan(&previous)
	return previous, persistence(err)
}
func (t *transaction) SettleModelCall(ctx context.Context, id, state string) error {
	_, err := t.tx.Exec(ctx, `UPDATE challenge.model_calls SET state=$2 WHERE id=$1`, id, state)
	return persistence(err)
}
func (t *transaction) FindResult(ctx context.Context, ref *domain.AttemptRef) (application.ResultReceipt, error) {
	var v application.ResultReceipt
	err := t.tx.QueryRow(ctx, `SELECT result_digest,worker_id,lease_epoch,work_item_id FROM challenge.receipts WHERE attempt_id=$1 AND run_id=$2`, ref.AttemptId, ref.RunId).Scan(&v.Digest, &v.Worker, &v.Epoch, &v.WorkID)
	return v, persistence(err)
}
func (t *transaction) AddResult(ctx context.Context, ref *domain.AttemptRef, owner, digest string) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO challenge.receipts(attempt_id,run_id,worker_id,lease_epoch,work_item_id,result_digest) VALUES($1,$2,$3,$4,$5,$6)`, ref.AttemptId, ref.RunId, owner, ref.LeaseEpoch, ref.WorkItemId, digest)
	return persistence(err)
}
