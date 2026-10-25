package gateway

import (
	"crypto/rand"
	"encoding/hex"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	discoverypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/discovery/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"net/http"
	"net/url"
	"strconv"
)

func commentJSON(c *pb.PublicComment) map[string]any {
	return map[string]any{"id": c.Id, "taskId": c.TaskId, "author": publicAuthorJSON(c.Author), "body": c.Body, "createdAt": timestampJSON(c.CreatedAt)}
}
func (h *Handler) listComments(w http.ResponseWriter, r *http.Request) {
	limit, cursor, reason := publicEntryQuery(r.URL.RawQuery)
	if reason != "" {
		problem(w, 400, reason)
		return
	}
	actor, ok := h.publicContentActor(w, r, contract.ListComments)
	if !ok {
		return
	}
	v, e := h.options.Content.ListComments(r.Context(), &pb.ListCommentsRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), Limit: limit, Cursor: cursor})
	if e != nil {
		contentError(w, e)
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, c := range v.Items {
		items = append(items, commentJSON(c))
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)})
}

// Optional operation key supports safe retries without making clients invent a
// key for ordinary comments/reports. Never accept multiple ambiguous headers.
func operationKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	if len(r.Header.Values("Idempotency-Key")) > 1 {
		problem(w, 400, "invalid_idempotency_key")
		return "", false
	}
	if key := r.Header.Get("Idempotency-Key"); key != "" {
		return key, true
	}
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		problem(w, 503, "internal_error")
		return "", false
	}
	return hex.EncodeToString(b[:]), true
}
func (h *Handler) createComment(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.contentActor(w, r, contract.CreateComment)
	if !ok {
		return
	}
	var q struct {
		Body string `json:"body"`
	}
	if !decode(w, r, &q) {
		return
	}
	key, ok := operationKey(w, r)
	if !ok {
		return
	}
	v, e := h.options.Content.CreateComment(r.Context(), &pb.CreateCommentRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), Body: q.Body, IdempotencyKey: key})
	if e != nil {
		contentError(w, e)
		return
	}
	jsonResponse(w, 201, commentJSON(v.Comment))
}
func discoveryQuery(raw string) (*discoverypb.ListTasksRequest, string) {
	q, e := url.ParseQuery(raw)
	if e != nil {
		return nil, "invalid_request"
	}
	for k, v := range q {
		if (k != "limit" && k != "cursor" && k != "order" && k != "category" && k != "q") || len(v) != 1 {
			return nil, "invalid_request"
		}
	}
	limit := 20
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil || limit < 1 || limit > 100 {
			return nil, "invalid_request"
		}
	}
	out := &discoverypb.ListTasksRequest{Limit: int32(limit), Cursor: q.Get("cursor"), Order: q.Get("order")}
	if q.Has("q") {
		query := q.Get("q")
		out.Query = &query
	}
	if len(out.Cursor) > 2048 {
		return nil, "invalid_cursor"
	}
	if q.Has("category") {
		c := q.Get("category")
		if c == "" || len(c) > 12000 {
			return nil, "invalid_category"
		}
		out.Category = &c
	}
	if out.Order != "" && out.Order != "recommended" && out.Order != "hot" {
		return nil, "invalid_request"
	}
	return out, ""
}
func (h *Handler) listTasks(w http.ResponseWriter, r *http.Request) {
	q, reason := discoveryQuery(r.URL.RawQuery)
	if reason != "" {
		problem(w, 400, reason)
		return
	}
	actor, ok := h.publicContentActor(w, r, contract.ListTasks)
	if !ok {
		return
	}
	if h.options.Discovery == nil {
		problem(w, 503, "discovery_unavailable")
		return
	}
	q.ActorAssertion = actor
	v, e := h.options.Discovery.ListTasks(r.Context(), q)
	if e != nil {
		rpcErrorFor(w, e, "discovery_unavailable")
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, t := range v.Items {
		items = append(items, publicTaskJSON(t))
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)})
}
func boardGroupJSON(g *discoverypb.BoardGroup) map[string]any {
	return map[string]any{"publishedTasks": g.GetPublishedTasks(), "undeterminedTasks": g.GetUndeterminedTasks(), "humanLeadingTasks": g.GetHumanLeadingTasks(), "agentLeadingTasks": g.GetAgentLeadingTasks()}
}
func (h *Handler) getPublicBoard(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.publicContentActor(w, r, contract.ReadBoard)
	if !ok {
		return
	}
	if h.options.Discovery == nil {
		problem(w, 503, "discovery_unavailable")
		return
	}
	v, e := h.options.Discovery.GetPublicBoard(r.Context(), &discoverypb.GetPublicBoardRequest{ActorAssertion: actor})
	if e != nil {
		rpcErrorFor(w, e, "discovery_unavailable")
		return
	}
	categories := make([]any, 0, len(v.Categories))
	for _, c := range v.Categories {
		categories = append(categories, map[string]any{"category": c.Category, "summary": boardGroupJSON(c.Summary)})
	}
	jsonResponse(w, 200, map[string]any{"overall": boardGroupJSON(v.Overall), "categories": categories, "computedAt": timestampJSON(v.ComputedAt)})
}
