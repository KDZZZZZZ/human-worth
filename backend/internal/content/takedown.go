package content

import (
	"context"
	"errors"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func moderationTarget(ctx context.Context, q contentQuerier, kind, id string) (string, bool, error) {
	var task string
	var visible bool
	var e error
	switch kind {
	case "task":
		e = q.QueryRow(ctx, `SELECT task_id,state='published' AND `+catalogPublic+` FROM content.reviewed_tasks t WHERE task_id=$1`, id).Scan(&task, &visible)
	case "entry":
		e = q.QueryRow(ctx, `SELECT t.task_id,t.state='published' AND `+catalogPublic+` AND e.state='approved' FROM content.reviewed_tasks t JOIN content.reviewed_entries e ON e.task_id=t.task_id WHERE e.id=$1`, id).Scan(&task, &visible)
	case "comment":
		e = q.QueryRow(ctx, `SELECT t.task_id,t.state='published' AND `+catalogPublic+` AND c.state='visible' FROM content.reviewed_tasks t JOIN content.comments c ON c.task_id=t.task_id WHERE c.id=$1`, id).Scan(&task, &visible)
	default:
		return "", false, invalid("invalid_moderation_target")
	}
	if errors.Is(e, pgx.ErrNoRows) {
		return "", false, status.Error(codes.NotFound, "content_not_found")
	}
	return task, visible, storage(e)
}
func (s *Server) CheckModerationTarget(ctx context.Context, r *pb.CheckModerationTargetRequest) (*pb.CheckModerationTargetResponse, error) {
	if e := requireContentCaller(ctx, "moderation"); e != nil {
		return nil, e
	}
	task, visible, e := moderationTarget(ctx, s.DB, r.GetTargetKind(), r.GetTargetId())
	if e != nil {
		return nil, e
	}
	return &pb.CheckModerationTargetResponse{TaskId: task, Visible: visible}, nil
}
func (s *Server) localApplyTakedown(ctx context.Context, r *pb.ApplyTakedownRequest) (*pb.ApplyTakedownResponse, error) {
	if e := decisionCaller(ctx); e != nil {
		return nil, e
	}
	if !keyPattern.MatchString(r.GetDecisionId()) {
		return nil, invalid("invalid_decision")
	}
	tx, e := s.beginCatalogTx(ctx)
	if e != nil {
		return nil, storage(e)
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7185429004))`, r.DecisionId); e != nil {
		return nil, storage(e)
	}
	var oldKind, oldID, oldTask string
	e = tx.QueryRow(ctx, `SELECT target_kind,target_id,task_id FROM content.takedown_receipts WHERE decision_id=$1`, r.DecisionId).Scan(&oldKind, &oldID, &oldTask)
	if e == nil {
		if oldKind != r.TargetKind || oldID != r.TargetId {
			return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
		}
		return &pb.ApplyTakedownResponse{DecisionId: r.DecisionId, TaskId: oldTask}, nil
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, storage(e)
	}
	task, _, e := moderationTarget(ctx, tx, r.TargetKind, r.TargetId)
	if e != nil {
		return nil, e
	}
	var id string
	if e = tx.QueryRow(ctx, `SELECT id FROM content.task_drafts WHERE id=$1 FOR UPDATE`, task).Scan(&id); e != nil {
		return nil, storage(e)
	}
	if e = tx.QueryRow(ctx, `SELECT task_id FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE`, task).Scan(&id); e != nil {
		return nil, storage(e)
	}
	switch r.TargetKind {
	case "task":
		_, e = tx.Exec(ctx, `UPDATE content.reviewed_tasks SET state='taken_down' WHERE task_id=$1`, task)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE content.task_drafts SET state='taken_down',updated_at=clock_timestamp() WHERE id=$1 AND review_id IS NOT NULL`, task)
		}
	case "entry":
		_, e = tx.Exec(ctx, `UPDATE content.reviewed_entries SET state='approval_revoked' WHERE id=$1`, r.TargetId)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE content.entry_submissions SET state='approval_revoked',updated_at=clock_timestamp() WHERE id=$1 AND state='approved'`, r.TargetId)
		}
	case "comment":
		_, e = tx.Exec(ctx, `UPDATE content.comments SET state='taken_down' WHERE id=$1`, r.TargetId)
	}
	if e != nil {
		return nil, storage(e)
	}
	_, e = tx.Exec(ctx, `UPDATE content.reviewed_tasks SET revision=revision+1,catalog_revision=catalog_revision+1,updated_at=clock_timestamp() WHERE task_id=$1`, task)
	if e != nil {
		return nil, storage(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO content.takedown_receipts(decision_id,target_kind,target_id,task_id) VALUES($1,$2,$3,$4)`, r.DecisionId, r.TargetKind, r.TargetId, task)
	if e != nil {
		return nil, storage(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, storage(e)
	}
	return &pb.ApplyTakedownResponse{DecisionId: r.DecisionId, TaskId: task}, nil
}

func (s *Server) ApplyTakedown(ctx context.Context, r *pb.ApplyTakedownRequest) (*pb.ApplyTakedownResponse, error) {
	if e := requireContentCaller(ctx, "moderation"); e != nil {
		return nil, e
	}
	if !keyPattern.MatchString(r.GetDecisionId()) {
		return nil, invalid("invalid_decision")
	}
	task, _, e := moderationTarget(ctx, s.DB, r.GetTargetKind(), r.GetTargetId())
	if e != nil {
		return nil, e
	}
	out, e := s.changeCatalog(ctx, "takedown", task, r)
	if e != nil {
		return nil, e
	}
	return out.(*pb.ApplyTakedownResponse), nil
}
