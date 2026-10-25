package moderation

import (
	"context"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"log/slog"
	"time"
)

const actionAuditSQL = `SELECT id,actor_id,'take_down_'||target_kind,target_id,CASE state WHEN 'applied' THEN 'succeeded' WHEN 'blocked' THEN 'failed' ELSE 'pending' END,reason,created_at FROM moderation.actions`

func readAudit(row scanner) (*pb.AuditEvent, error) {
	v := &pb.AuditEvent{}
	var created time.Time
	e := row.Scan(&v.Id, &v.ActorId, &v.Action, &v.TargetId, &v.Outcome, &v.Reason, &created)
	if e != nil {
		return nil, unavailable(e)
	}
	v.CreatedAt = timestamppb.New(created)
	return v, nil
}
func (s *Server) deliverAction(ctx context.Context, wanted string) (platform.OutboxResult, error) {
	out := platform.OutboxResult{Outcome: "idle", Stage: "claim"}
	if s.Content == nil {
		return out, nil
	}
	token, e := randomID("lease")
	if e != nil {
		return out, e
	}
	var id, kind, target string
	e = s.DB.QueryRow(ctx, `UPDATE moderation.actions a SET lease_token=$1,available_at=clock_timestamp()+interval '15 seconds' FROM (SELECT id FROM moderation.actions WHERE state='pending' AND available_at<=clock_timestamp() AND ($2='' OR id=$2) ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) q WHERE a.id=q.id RETURNING a.id,a.target_kind,a.target_id`, token, wanted).Scan(&id, &kind, &target)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, nil
	}
	if e != nil {
		return out, unavailable(e)
	}
	out.HadWork = true
	out.ItemID = id
	out.Stage = "call_content"
	receipt, e := s.Content.ApplyTakedown(ctx, &contentpb.ApplyTakedownRequest{DecisionId: id, TargetKind: kind, TargetId: target})
	state := "applied"
	if e != nil {
		switch status.Code(e) {
		case codes.NotFound, codes.InvalidArgument, codes.AlreadyExists:
			state = "blocked"
			out.Reason = status.Convert(e).Message()
		default:
			out.Cause = e
			return out, e
		}
	} else if receipt.GetDecisionId() != id || receipt.GetTaskId() == "" {
		return out, status.Error(codes.Unavailable, "invalid_takedown_receipt")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return out, unavailable(e)
	}
	defer tx.Rollback(ctx)
	tag, e := tx.Exec(ctx, `UPDATE moderation.actions SET state=$3,applied_at=CASE WHEN $3='applied' THEN clock_timestamp() ELSE NULL END,lease_token=NULL WHERE id=$1 AND lease_token=$2 AND state='pending'`, id, token, state)
	if e != nil {
		return out, unavailable(e)
	}
	if tag.RowsAffected() == 0 {
		out.Outcome = "lease_lost"
		return out, nil
	}
	if state == "applied" {
		_, e = tx.Exec(ctx, `UPDATE moderation.reports SET status='actioned',resolved_at=clock_timestamp() WHERE action_id=$1 AND status='open'`, id)
		if e != nil {
			return out, unavailable(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return out, unavailable(e)
	}
	out.Outcome = state
	return out, nil
}
func (s *Server) RunActionDelivery(ctx context.Context, logger *slog.Logger) {
	platform.RunOutbox(ctx, logger, "moderation_takedown", func(c context.Context) (platform.OutboxResult, error) { return s.deliverAction(c, "") })
}
func (s *Server) GetDashboard(ctx context.Context, r *pb.GetDashboardRequest) (*pb.GetDashboardResponse, error) {
	if _, e := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_GetDashboard_FullMethodName); e != nil {
		return nil, e
	}
	v := &pb.GetDashboardResponse{}
	e := s.DB.QueryRow(ctx, `SELECT (SELECT count(*) FROM moderation.reviews WHERE status='pending' AND object_kind='task'),(SELECT count(*) FROM moderation.reviews WHERE status='pending' AND object_kind='entry'),(SELECT count(*) FROM moderation.reports WHERE status='open')`).Scan(&v.PendingTaskReviews, &v.PendingEntryReviews, &v.OpenReports)
	if e != nil {
		return nil, unavailable(e)
	}
	return v, nil
}
func (s *Server) ListAuditEvents(ctx context.Context, r *pb.ListAuditEventsRequest) (*pb.ListAuditEventsResponse, error) {
	if _, e := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_ListAuditEvents_FullMethodName); e != nil {
		return nil, e
	}
	limit := r.Limit
	if limit == 0 {
		limit = 21
	}
	if limit < 1 || limit > 101 {
		return nil, invalid("invalid_request")
	}
	before := time.Now().Add(time.Second)
	id := "~"
	if r.BeforeTime != nil {
		if r.BeforeTime.CheckValid() != nil || len(r.BeforeId) > 256 {
			return nil, invalid("invalid_cursor")
		}
		before = r.BeforeTime.AsTime()
		id = r.BeforeId
	}
	rows, e := s.DB.Query(ctx, `SELECT * FROM (`+actionAuditSQL+`
 UNION ALL SELECT decision_id,reviewer_id,'review_'||decision,COALESCE(snapshot->'submission'->>'id',task_id),CASE delivery_state WHEN 'applied' THEN 'succeeded' WHEN 'blocked' THEN 'failed' ELSE 'pending' END,reason,decided_at FROM moderation.reviews WHERE decision_id IS NOT NULL
 UNION ALL SELECT 'resolve_'||id,resolver_id,'dismiss_report',id,'succeeded',resolution_reason,resolved_at FROM moderation.reports WHERE status='dismissed') events WHERE (created_at,id)<($1,$2) ORDER BY created_at DESC,id DESC LIMIT $3`, before, id, limit)
	if e != nil {
		return nil, unavailable(e)
	}
	defer rows.Close()
	out := &pb.ListAuditEventsResponse{}
	for rows.Next() {
		v, e := readAudit(rows)
		if e != nil {
			return nil, e
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, unavailable(e)
	}
	return out, nil
}
