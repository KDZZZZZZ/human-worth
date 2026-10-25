package gateway

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// An explicitly empty session credential is invalid, not an anonymous request.
func (h *Handler) publicContentActor(w http.ResponseWriter, r *http.Request, operation contract.OperationID) (string, bool) {
	session, err := cookie(r, SessionCookie)
	if err != nil {
		problem(w, 400, "invalid_request")
		return "", false
	}
	// net/http drops malformed cookie values. Presence must survive parsing:
	// an invalid supplied session must not silently become anonymous access.
	present := 0
	for _, line := range r.Header.Values("Cookie") {
		for _, field := range strings.Split(line, ";") {
			name, _, _ := strings.Cut(strings.TrimSpace(field), "=")
			if strings.TrimSpace(name) == SessionCookie {
				present++
			}
		}
	}
	if present > 1 || (present == 1 && hasAuthorization(r)) {
		problem(w, 400, "invalid_request")
		return "", false
	}
	if present == 1 && session == "" {
		problem(w, 401, "unauthenticated")
		return "", false
	}
	definition, ok := contract.LookupOperation(operation)
	if !ok {
		problem(w, 503, "internal_error")
		return "", false
	}
	if definition.Policy.Audience == "content" {
		return h.contentActor(w, r, operation)
	}
	return h.actorFor(w, r, operation)
}
func timestampJSON(t *timestamppb.Timestamp) any {
	if t == nil {
		return nil
	}
	return t.AsTime().UTC().Format(time.RFC3339Nano)
}
func publicAuthorJSON(a *pb.PublicAuthor) map[string]any {
	var name any
	if a.DisplayName != nil {
		name = *a.DisplayName
	}
	return map[string]any{"id": a.Id, "displayName": name}
}
func (h *Handler) getPublicTask(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	actor, ok := h.publicContentActor(w, r, contract.ReadTask)
	if !ok {
		return
	}
	result, err := h.options.Content.GetTask(r.Context(), &pb.GetTaskRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId")})
	if err != nil {
		contentError(w, err)
		return
	}
	t := result.GetTask()
	if t == nil || t.Author == nil {
		problem(w, 503, "content_unavailable")
		return
	}
	jsonResponse(w, 200, publicTaskJSON(t))
}
func publicTaskJSON(t *pb.PublicTask) map[string]any {
	var category, heat any
	if t.Category != nil {
		category = *t.Category
	}
	if t.Heat != nil {
		heat = *t.Heat
	}
	attachments := make([]any, 0, len(t.TaskAttachments))
	for _, a := range t.TaskAttachments {
		attachments = append(attachments, map[string]any{"id": a.Id, "filename": a.Filename, "mediaType": a.MediaType, "bytes": a.Bytes, "sha256": a.Sha256})
	}
	return map[string]any{"id": t.Id, "revision": t.Revision, "title": t.Title, "summary": t.Summary, "description": t.Description, "category": category, "author": publicAuthorJSON(t.Author), "heat": heat, "publishedAt": timestampJSON(t.PublishedAt), "taskAttachments": attachments}
}
func publicEntryQuery(raw string) (int32, string, string) {
	q, err := url.ParseQuery(raw)
	if err != nil {
		return 0, "", "invalid_request"
	}
	for k, v := range q {
		if (k != "limit" && k != "cursor") || len(v) != 1 {
			return 0, "", "invalid_request"
		}
	}
	limit := 20
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return 0, "", "invalid_request"
		}
	}
	if len(q.Get("cursor")) > maxSubmissionCursorLength {
		return 0, "", "invalid_cursor"
	}
	return int32(limit), q.Get("cursor"), ""
}
func (h *Handler) listPublicEntries(w http.ResponseWriter, r *http.Request) {
	limit, cursor, reason := publicEntryQuery(r.URL.RawQuery)
	if reason != "" {
		problem(w, 400, reason)
		return
	}
	actor, ok := h.publicContentActor(w, r, contract.ListApprovedEntries)
	if !ok {
		return
	}
	result, err := h.options.Content.ListApprovedEntries(r.Context(), &pb.ListApprovedEntriesRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), Limit: limit, Cursor: cursor})
	if err != nil {
		contentError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, e := range result.Items {
		if e == nil || e.Author == nil {
			problem(w, 503, "content_unavailable")
			return
		}
		item := approvedEntryJSON(e)
		if item == nil {
			problem(w, 503, "content_unavailable")
			return
		}
		items = append(items, item)
	}
	var next any
	if result.NextCursor != "" {
		next = result.NextCursor
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": next})
}

func approvedEntryJSON(e *pb.ApprovedEntry) map[string]any {
	artifacts := make([]map[string]any, 0, len(e.Artifacts))
	for _, a := range e.Artifacts {
		v := map[string]any{"kind": a.Kind}
		switch a.Kind {
		case "file":
			v["assetId"] = a.AssetId
		case "link":
			v["url"] = a.Url
		case "text":
			v["text"] = a.Text
		default:
			return nil
		}
		artifacts = append(artifacts, v)
	}
	return map[string]any{"id": e.Id, "taskId": e.TaskId, "side": e.Side, "title": e.Title, "description": e.Description, "source": e.Source, "artifacts": artifacts, "author": publicAuthorJSON(e.Author), "approvedAt": timestampJSON(e.ApprovedAt)}
}
