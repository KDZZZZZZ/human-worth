package moderation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"
	"unicode/utf8"
)

func validReason(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= 4000 && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}
func validTarget(t *pb.ModerationTarget) bool {
	return t != nil && (t.TargetKind == "task" || t.TargetKind == "entry" || t.TargetKind == "comment") && len(t.TargetId) > 0 && len(t.TargetId) <= 128 && validReason(t.Reason)
}
func validKey(k string) bool {
	if len(k) < 16 || len(k) > 128 {
		return false
	}
	for _, c := range k {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func actionDigest(t *pb.ModerationTarget) []byte {
	b, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(t)
	h := sha256.Sum256(b)
	return h[:]
}

const reportSelect = `SELECT r.id,r.target_kind,r.target_id,r.reason,r.status,r.resolution_reason,r.created_at,r.resolved_at,COALESCE(a.state,'none') FROM moderation.reports r LEFT JOIN moderation.actions a ON a.id=r.action_id`

func readReport(row scanner) (*pb.Report, error) {
	v := &pb.Report{Target: &pb.ModerationTarget{}}
	var created time.Time
	var resolved *time.Time
	e := row.Scan(&v.Id, &v.Target.TargetKind, &v.Target.TargetId, &v.Target.Reason, &v.Status, &v.ResolutionReason, &created, &resolved, &v.DeliveryState)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "report_not_found")
	}
	if e != nil {
		return nil, unavailable(e)
	}
	v.CreatedAt = timestamppb.New(created)
	if resolved != nil {
		v.ResolvedAt = timestamppb.New(*resolved)
	}
	return v, nil
}
func (s *Server) CreateReport(ctx context.Context, r *pb.CreateReportRequest) (*pb.CreateReportResponse, error) {
	if e := peer(ctx, "gateway"); e != nil {
		return nil, e
	}
	if s.Identity == nil {
		return nil, status.Error(codes.Unavailable, "identity_unavailable")
	}
	principal, e := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: r.GetActorAssertion(), FullMethod: pb.ModerationService_CreateReport_FullMethodName})
	if e != nil {
		return nil, e
	}
	p := principal.GetPrincipal()
	if p.GetClientKind() != identitypb.ClientKind_CLIENT_KIND_WEB_SESSION || p.GetAccountId() == "" {
		return nil, status.Error(codes.PermissionDenied, "web_session_required")
	}
	if !validTarget(r.Target) || !validKey(r.IdempotencyKey) {
		return nil, invalid("invalid_report")
	}
	// Recover a committed receipt before checking mutable target visibility.
	// A successfully handled report must remain retryable after its takedown.
	var previousID string
	var previousHash []byte
	e = s.DB.QueryRow(ctx, `SELECT id,request_hash FROM moderation.reports WHERE reporter_id=$1 AND create_key=$2`, p.AccountId, r.IdempotencyKey).Scan(&previousID, &previousHash)
	if e == nil {
		if !bytes.Equal(previousHash, actionDigest(r.Target)) {
			return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
		}
		v, err := readReport(s.DB.QueryRow(ctx, reportSelect+` WHERE r.id=$1`, previousID))
		return &pb.CreateReportResponse{Report: v}, err
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return nil, unavailable(e)
	}
	if s.Content == nil {
		return nil, status.Error(codes.Unavailable, "content_unavailable")
	}
	visible, e := s.Content.CheckModerationTarget(ctx, &contentpb.CheckModerationTargetRequest{TargetKind: r.Target.TargetKind, TargetId: r.Target.TargetId})
	if e != nil {
		return nil, e
	}
	if !visible.Visible {
		return nil, status.Error(codes.NotFound, "content_not_found")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, unavailable(e)
	}
	defer tx.Rollback(ctx)
	id, e := randomID("rpt")
	if e != nil {
		return nil, e
	}
	hash := actionDigest(r.Target)
	_, e = tx.Exec(ctx, `INSERT INTO moderation.reports(id,reporter_id,target_kind,target_id,reason,create_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(reporter_id,create_key) DO NOTHING`, id, p.AccountId, r.Target.TargetKind, r.Target.TargetId, r.Target.Reason, r.IdempotencyKey, hash)
	if e != nil {
		return nil, unavailable(e)
	}
	var old []byte
	e = tx.QueryRow(ctx, `SELECT id,request_hash FROM moderation.reports WHERE reporter_id=$1 AND create_key=$2`, p.AccountId, r.IdempotencyKey).Scan(&id, &old)
	if e != nil {
		return nil, unavailable(e)
	}
	if !bytes.Equal(old, hash) {
		return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
	}
	v, e := readReport(tx.QueryRow(ctx, reportSelect+` WHERE r.id=$1`, id))
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, unavailable(e)
	}
	return &pb.CreateReportResponse{Report: v}, nil
}

type reportCursor struct {
	Status string    `json:"status"`
	Time   time.Time `json:"time"`
	ID     string    `json:"id"`
}

func (s *Server) ListReports(ctx context.Context, r *pb.ListReportsRequest) (*pb.ListReportsResponse, error) {
	if _, e := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_ListReports_FullMethodName); e != nil {
		return nil, e
	}
	filter := r.Status
	if filter == "" {
		filter = "open"
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if (filter != "open" && filter != "dismissed" && filter != "actioned") || limit < 1 || limit > 100 {
		return nil, invalid("invalid_request")
	}
	c := reportCursor{Status: filter, Time: time.Now().Add(time.Second), ID: "~"}
	if r.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(r.Cursor)
		if e != nil || len(b) > 1024 || json.Unmarshal(b, &c) != nil || c.Status != filter || c.ID == "" || c.Time.IsZero() {
			return nil, invalid("invalid_cursor")
		}
	}
	rows, e := s.DB.Query(ctx, reportSelect+` WHERE r.status=$1 AND (r.created_at,r.id)<($2,$3) ORDER BY r.created_at DESC,r.id DESC LIMIT $4`, filter, c.Time, c.ID, limit+1)
	if e != nil {
		return nil, unavailable(e)
	}
	defer rows.Close()
	out := &pb.ListReportsResponse{}
	for rows.Next() {
		v, e := readReport(rows)
		if e != nil {
			return nil, e
		}
		if len(out.Items) == int(limit) {
			last := out.Items[len(out.Items)-1]
			b, _ := json.Marshal(reportCursor{filter, last.CreatedAt.AsTime(), last.Id})
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			break
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, unavailable(e)
	}
	return out, nil
}
func insertAction(ctx context.Context, tx pgx.Tx, actor, key string, target *pb.ModerationTarget) (string, error) {
	id, e := randomID("act")
	if e != nil {
		return "", e
	}
	hash := actionDigest(target)
	_, e = tx.Exec(ctx, `INSERT INTO moderation.actions(id,actor_id,target_kind,target_id,reason,create_key,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(actor_id,create_key) DO NOTHING`, id, actor, target.TargetKind, target.TargetId, target.Reason, key, hash)
	if e != nil {
		return "", unavailable(e)
	}
	var old []byte
	e = tx.QueryRow(ctx, `SELECT id,request_hash FROM moderation.actions WHERE actor_id=$1 AND create_key=$2`, actor, key).Scan(&id, &old)
	if e != nil {
		return "", unavailable(e)
	}
	if !bytes.Equal(hash, old) {
		return "", status.Error(codes.AlreadyExists, "idempotency_key_conflict")
	}
	return id, nil
}
func (s *Server) ResolveReport(ctx context.Context, r *pb.ResolveReportRequest) (*pb.ResolveReportResponse, error) {
	actor, e := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_ResolveReport_FullMethodName)
	if e != nil {
		return nil, e
	}
	if (r.Action != "dismiss" && r.Action != "take_down") || !validReason(r.Reason) {
		return nil, invalid("invalid_resolution")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, unavailable(e)
	}
	defer tx.Rollback(ctx)
	var kind, target string
	var oldAction, oldReason, actionID *string
	e = tx.QueryRow(ctx, `SELECT target_kind,target_id,resolution_action,resolution_reason,action_id FROM moderation.reports WHERE id=$1 FOR UPDATE`, r.ReportId).Scan(&kind, &target, &oldAction, &oldReason, &actionID)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "report_not_found")
	}
	if e != nil {
		return nil, unavailable(e)
	}
	if oldAction != nil {
		if *oldAction != r.Action || oldReason == nil || *oldReason != r.Reason {
			return nil, status.Error(codes.Aborted, "resolution_conflict")
		}
	} else {
		if r.Action == "take_down" {
			id, e := insertAction(ctx, tx, actor, "report_"+r.ReportId, &pb.ModerationTarget{TargetKind: kind, TargetId: target, Reason: r.Reason})
			if e != nil {
				return nil, e
			}
			actionID = &id
		}
		_, e = tx.Exec(ctx, `UPDATE moderation.reports SET resolution_action=$2,resolution_reason=$3,resolver_id=$4,action_id=$5,status=CASE WHEN $2='dismiss' THEN 'dismissed' ELSE 'open' END,resolved_at=CASE WHEN $2='dismiss' THEN clock_timestamp() ELSE NULL END WHERE id=$1`, r.ReportId, r.Action, r.Reason, actor, actionID)
		if e != nil {
			return nil, unavailable(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, unavailable(e)
	}
	if actionID != nil {
		_, _ = s.deliverAction(ctx, *actionID)
	}
	out, e := readReport(s.DB.QueryRow(ctx, reportSelect+` WHERE r.id=$1`, r.ReportId))
	if e != nil {
		return nil, e
	}
	return &pb.ResolveReportResponse{Report: out}, nil
}
func (s *Server) TakeDownContent(ctx context.Context, r *pb.TakeDownContentRequest) (*pb.TakeDownContentResponse, error) {
	actor, e := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_TakeDownContent_FullMethodName)
	if e != nil {
		return nil, e
	}
	if !validTarget(r.Target) || !validKey(r.IdempotencyKey) {
		return nil, invalid("invalid_takedown")
	}
	// Existence, not public visibility: retries after a successful takedown work.
	if s.Content == nil {
		return nil, status.Error(codes.Unavailable, "content_unavailable")
	}
	if _, e = s.Content.CheckModerationTarget(ctx, &contentpb.CheckModerationTargetRequest{TargetKind: r.Target.TargetKind, TargetId: r.Target.TargetId}); e != nil {
		return nil, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, unavailable(e)
	}
	defer tx.Rollback(ctx)
	id, e := insertAction(ctx, tx, actor, r.IdempotencyKey, r.Target)
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, unavailable(e)
	}
	_, _ = s.deliverAction(ctx, id)
	event, e := readAudit(s.DB.QueryRow(ctx, actionAuditSQL+` WHERE id=$1`, id))
	if e != nil {
		return nil, e
	}
	return &pb.TakeDownContentResponse{Event: event}, nil
}
