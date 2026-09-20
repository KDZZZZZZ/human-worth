package challenge

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate 只迁移 Challenge 的私有 schema，不创建其他业务模块的表。
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

// record 把数据库时间和版本带入一次事务；状态修改始终先锁 Run。
type record struct {
	*pb.StoredRun
	version       int64
	worker        string
	now, deadline time.Time
}

func id(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + hex.EncodeToString(b)
}
func hash(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func messageHash(m proto.Message) string {
	b, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(m)
	return hash(b)
}
func invalid(reason string) error  { return status.Error(codes.InvalidArgument, reason) }
func denied(reason string) error   { return status.Error(codes.PermissionDenied, reason) }
func conflict(reason string) error { return status.Error(codes.FailedPrecondition, reason) }
func unavailable() error           { return status.Error(codes.Unavailable, "challenge_unavailable") }
func storage(err error) error {
	if err == nil {
		return nil
	}
	if status.Code(err) != codes.Unknown {
		return err
	}
	return unavailable()
}
func terminal(s string) bool { return s == "completed" || s == "failed" || s == "cancelled" }

func load(ctx context.Context, tx pgx.Tx, runID string, lock bool) (*record, error) {
	query := "SELECT state,version,clock_timestamp(),deadline,worker_id FROM challenge.runs WHERE id=$1"
	if lock {
		query += " FOR UPDATE"
	}
	r := &record{StoredRun: &pb.StoredRun{}}
	var data []byte
	if err := tx.QueryRow(ctx, query, runID).Scan(&data, &r.version, &r.now, &r.deadline, &r.worker); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "run_not_found")
		}
		return nil, storage(err)
	}
	if proto.Unmarshal(data, r.StoredRun) != nil {
		return nil, unavailable()
	}
	return r, nil
}
func save(ctx context.Context, tx pgx.Tx, r *record) error {
	r.Run.UpdatedAt = timestamppb.New(r.now)
	data, err := proto.Marshal(r.StoredRun)
	if err != nil {
		return unavailable()
	}
	var kind int32
	var lease *time.Time
	leased := false
	if a := r.Assignment; a != nil {
		kind = int32(a.Kind)
		if a.Attempt != nil {
			leased = true
			t := a.LeaseExpiresAt.AsTime()
			lease = &t
		}
	}
	if !leased {
		r.worker = ""
	}
	_, err = tx.Exec(ctx, `UPDATE challenge.runs SET status=$2,pending=$3,kind=$4,leased=$5,lease_until=$6,state=$7,worker_id=$8,version=version+1,updated_at=clock_timestamp() WHERE id=$1`, r.Run.Id, r.Run.Status, r.Pending, kind, leased, lease, data, r.worker)
	return storage(err)
}

// mutate 只包装一个真实数据库事务，业务状态与后续待办在同一次提交中落库。
func (s *Server) mutate(ctx context.Context, runID string, f func(pgx.Tx, *record) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return storage(err)
	}
	defer tx.Rollback(ctx)
	r, err := load(ctx, tx, runID, true)
	if err != nil {
		return err
	}
	if err = f(tx, r); err != nil {
		return err
	}
	if err = save(ctx, tx, r); err != nil {
		return err
	}
	return storage(tx.Commit(ctx))
}
func audit(ctx context.Context, tx pgx.Tx, runID, actor, action, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO challenge.audit_events(run_id,actor_id,action,reason) VALUES($1,$2,$3,$4)`, runID, actor, action, reason)
	return storage(err)
}

// finish 先撤销工作和授权，再由持久化待办关闭 Content 登记通道。
func finish(r *record, target, reason string) {
	r.Run.Status = "finalizing"
	if target == "cancelled" {
		r.Run.Status = "cancelling"
	}
	r.Run.FailureReason = reason
	r.TerminalTarget = target
	r.Pending = "close"
	r.Assignment = nil
	r.GrantHash = ""
	r.ExecutionInstanceId = ""
}
