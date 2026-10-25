package gateway

import (
	"encoding/base64"
	"encoding/json"
	challengepb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"net/url"
	"sort"
	"time"
)

func (h *Handler) adminActor(w http.ResponseWriter, r *http.Request, operation contract.OperationID) (string, bool) {
	actor, ok := h.actorFor(w, r, operation)
	if !ok {
		return "", false
	}
	if h.options.Moderation == nil {
		problem(w, 503, "moderation_unavailable")
		return "", false
	}
	return actor, true
}
func reportJSON(v *pb.Report) map[string]any {
	return map[string]any{"id": v.Id, "target": map[string]any{"targetKind": v.Target.TargetKind, "targetId": v.Target.TargetId, "reason": v.Target.Reason}, "status": v.Status, "resolutionReason": v.ResolutionReason, "createdAt": timestampJSON(v.CreatedAt), "resolvedAt": timestampJSON(v.ResolvedAt), "deliveryState": v.DeliveryState}
}
func auditJSON(v *pb.AuditEvent) map[string]any {
	return map[string]any{"id": v.Id, "actorId": v.ActorId, "action": v.Action, "targetId": v.TargetId, "outcome": v.Outcome, "reason": v.Reason, "createdAt": timestampJSON(v.CreatedAt)}
}
func moderationInput(w http.ResponseWriter, r *http.Request) (*pb.ModerationTarget, bool) {
	var q struct {
		Kind   string `json:"targetKind"`
		ID     string `json:"targetId"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &q) {
		return nil, false
	}
	return &pb.ModerationTarget{TargetKind: q.Kind, TargetId: q.ID, Reason: q.Reason}, true
}
func (h *Handler) createReport(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.adminActor(w, r, contract.CreateReport)
	if !ok {
		return
	}
	q, ok := moderationInput(w, r)
	if !ok {
		return
	}
	key, ok := operationKey(w, r)
	if !ok {
		return
	}
	v, e := h.options.Moderation.CreateReport(r.Context(), &pb.CreateReportRequest{ActorAssertion: actor, Target: q, IdempotencyKey: key})
	if e != nil {
		rpcErrorFor(w, e, "moderation_unavailable")
		return
	}
	jsonResponse(w, 201, reportJSON(v.Report))
}
func (h *Handler) listReports(w http.ResponseWriter, r *http.Request) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		problem(w, 400, "invalid_request")
		return
	}
	filter := q.Get("status")
	if len(q["status"]) > 1 {
		problem(w, 400, "invalid_request")
		return
	}
	q.Del("status")
	limit, cursor, reason := publicEntryQuery(q.Encode())
	if reason != "" {
		problem(w, 400, reason)
		return
	}
	actor, ok := h.adminActor(w, r, contract.ListReports)
	if !ok {
		return
	}
	v, e := h.options.Moderation.ListReports(r.Context(), &pb.ListReportsRequest{ActorAssertion: actor, Status: filter, Limit: limit, Cursor: cursor})
	if e != nil {
		rpcErrorFor(w, e, "moderation_unavailable")
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, r := range v.Items {
		items = append(items, reportJSON(r))
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)})
}
func (h *Handler) resolveReport(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.adminActor(w, r, contract.ResolveReport)
	if !ok {
		return
	}
	var q struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &q) {
		return
	}
	v, e := h.options.Moderation.ResolveReport(r.Context(), &pb.ResolveReportRequest{ActorAssertion: actor, ReportId: r.PathValue("reportId"), Action: q.Action, Reason: q.Reason})
	if e != nil {
		rpcErrorFor(w, e, "moderation_unavailable")
		return
	}
	code := 200
	if v.Report.DeliveryState == "pending" {
		code = 202
	}
	jsonResponse(w, code, reportJSON(v.Report))
}
func (h *Handler) takeDownContent(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.adminActor(w, r, contract.TakeDownContent)
	if !ok {
		return
	}
	q, ok := moderationInput(w, r)
	if !ok {
		return
	}
	key, ok := operationKey(w, r)
	if !ok {
		return
	}
	v, e := h.options.Moderation.TakeDownContent(r.Context(), &pb.TakeDownContentRequest{ActorAssertion: actor, Target: q, IdempotencyKey: key})
	if e != nil {
		rpcErrorFor(w, e, "moderation_unavailable")
		return
	}
	code := 200
	if v.Event.Outcome == "pending" {
		code = 202
	}
	jsonResponse(w, code, auditJSON(v.Event))
}
func (h *Handler) getAdminDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.adminActor(w, r, contract.AdminDashboard)
	if !ok {
		return
	}
	v, e := h.options.Moderation.GetDashboard(r.Context(), &pb.GetDashboardRequest{ActorAssertion: actor})
	if e != nil {
		rpcErrorFor(w, e, "moderation_unavailable")
		return
	}
	runs := &challengepb.GetRunSummaryResponse{}
	if h.options.Challenge != nil {
		a, ok := h.actorFor(w, r, contract.GetRunSummary)
		if !ok {
			return
		}
		runs, e = h.options.Challenge.GetRunSummary(r.Context(), &challengepb.GetRunSummaryRequest{ActorAssertion: a})
		if e != nil {
			rpcErrorFor(w, e, "challenge_unavailable")
			return
		}
	}
	jsonResponse(w, 200, map[string]any{"pendingTaskReviews": v.PendingTaskReviews, "pendingEntryReviews": v.PendingEntryReviews, "openReports": v.OpenReports, "activeRuns": runs.ActiveRuns, "failedRuns": runs.FailedRuns})
}

type auditCursor struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

func (h *Handler) listAuditEvents(w http.ResponseWriter, r *http.Request) {
	limit, cursor, reason := publicEntryQuery(r.URL.RawQuery)
	if reason != "" {
		problem(w, 400, reason)
		return
	}
	actor, ok := h.adminActor(w, r, contract.ListAuditEvents)
	if !ok {
		return
	}
	before := auditCursor{Time: time.Now().Add(time.Second), ID: "~"}
	if cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || len(b) > 1024 || json.Unmarshal(b, &before) != nil || before.Time.IsZero() || before.ID == "" {
			problem(w, 400, "invalid_cursor")
			return
		}
	}
	v, e := h.options.Moderation.ListAuditEvents(r.Context(), &pb.ListAuditEventsRequest{ActorAssertion: actor, Limit: limit + 1, BeforeTime: timestamppb.New(before.Time), BeforeId: before.ID})
	if e != nil {
		rpcErrorFor(w, e, "moderation_unavailable")
		return
	}
	if h.options.Challenge != nil {
		a, ok := h.actorFor(w, r, contract.ListChallengeAuditEvents)
		if !ok {
			return
		}
		c, e := h.options.Challenge.ListAuditEvents(r.Context(), &challengepb.ListAuditEventsRequest{ActorAssertion: a, Limit: limit + 1, BeforeTime: timestamppb.New(before.Time), BeforeId: before.ID})
		if e != nil {
			rpcErrorFor(w, e, "challenge_unavailable")
			return
		}
		for _, event := range c.Items {
			reason := event.Reason
			v.Items = append(v.Items, &pb.AuditEvent{Id: event.Id, ActorId: event.ActorId, Action: event.Action, TargetId: event.TargetId, Outcome: event.Outcome, Reason: &reason, CreatedAt: event.CreatedAt})
		}
	}
	sort.Slice(v.Items, func(i, j int) bool {
		a, b := v.Items[i], v.Items[j]
		if a.CreatedAt.AsTime().Equal(b.CreatedAt.AsTime()) {
			return a.Id > b.Id
		}
		return a.CreatedAt.AsTime().After(b.CreatedAt.AsTime())
	})
	next := ""
	if len(v.Items) > int(limit) {
		v.Items = v.Items[:limit]
		last := v.Items[len(v.Items)-1]
		b, _ := json.Marshal(auditCursor{last.CreatedAt.AsTime(), last.Id})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	items := make([]any, 0, len(v.Items))
	for _, event := range v.Items {
		items = append(items, auditJSON(event))
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": nullable(next)})
}
