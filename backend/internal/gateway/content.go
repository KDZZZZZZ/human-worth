package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"google.golang.org/protobuf/encoding/protojson"
)

const maxSubmissionCursorLength = 2048
const maxCategoryBytes = 12000

// HTTP remains handwritten. Do not accept protobuf snake_case aliases, enum
// numbers, omitted required values, or a client-provided author/actor/state.
func draftInput(raw json.RawMessage) (*pb.TaskDraftInput, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, false
	}
	for _, name := range []string{"title", "summary", "description", "entries"} {
		if len(object[name]) == 0 || string(object[name]) == "null" {
			return nil, false
		}
	}
	if !httpDraftNames(raw) {
		return nil, false
	}
	var entries []json.RawMessage
	if json.Unmarshal(object["entries"], &entries) != nil {
		return nil, false
	}
	input := &pb.TaskDraftInput{}
	if protojson.Unmarshal(raw, input) != nil {
		return nil, false
	}
	return input, true
}
func httpDraftNames(raw []byte) bool {
	// Only proto field aliases differ from the OpenAPI object shape. protojson
	// rejects unknown fields and duplicates; explicitly reject snake_case aliases.
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	var walk func(any) bool
	walk = func(v any) bool {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				if child == nil && k != "category" {
					return false
				}
				if k == "asset_id" || k == "cloud_use" || k == "agent_configuration" || k == "task_attachments" || k == "cloud_use_allowed" {
					return false
				}
				if k != "parameters" && !walk(child) {
					return false
				}
			}
		case []any:
			for _, child := range v {
				if !walk(child) {
					return false
				}
			}
		}
		return true
	}
	return walk(value)
}
func (h *Handler) contentActor(w http.ResponseWriter, r *http.Request, operation contract.OperationID) (string, bool) {
	if h.options.Content == nil {
		problem(w, 503, "content_unavailable")
		return "", false
	}
	return h.actorFor(w, r, operation)
}
func (h *Handler) assetActor(w http.ResponseWriter, r *http.Request, operation contract.OperationID) (string, bool) {
	if h.options.Asset == nil {
		problem(w, 503, "asset_unavailable")
		return "", false
	}
	return h.actorFor(w, r, operation)
}
func hasFileArtifact(input *pb.TaskDraftInput) bool {
	if len(input.GetTaskAttachments()) > 0 {
		return true
	}
	for _, entry := range input.GetEntries() {
		for _, artifact := range entry.GetArtifacts() {
			if artifact.GetKind() == "file" {
				return true
			}
		}
	}
	return false
}
func (h *Handler) createDraft(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, contract.CreateDraft)
	if !ok {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		problem(w, 400, "invalid_idempotency_key")
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	input, ok := draftInput(raw)
	if !ok {
		problem(w, 400, "invalid_draft")
		return
	}
	assetActor := ""
	if hasFileArtifact(input) {
		var assetOK bool
		assetActor, assetOK = h.assetActor(w, r, contract.CheckAssetOwnership)
		if !assetOK {
			return
		}
	}
	response, err := h.options.Content.CreateTaskDraft(r.Context(), &pb.CreateTaskDraftRequest{ActorAssertion: actor, AssetActorAssertion: assetActor, IdempotencyKey: r.Header.Get("Idempotency-Key"), Content: input})
	if err != nil {
		contentError(w, err)
		return
	}
	taskResponse(w, 201, response.Submission)
}
func submissionListRequest(rawQuery string) (*pb.ListMySubmissionsRequest, string) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return nil, "invalid_request"
	}
	allowed := map[string]bool{"cursor": true, "limit": true, "kind": true, "state": true, "category": true}
	for name, values := range query {
		if !allowed[name] || len(values) != 1 {
			return nil, "invalid_request"
		}
	}
	request := &pb.ListMySubmissionsRequest{Limit: 20, Cursor: query.Get("cursor")}
	if len(request.Cursor) > maxSubmissionCursorLength {
		return nil, "invalid_cursor"
	}
	if query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return nil, "invalid_request"
		}
		request.Limit = int32(limit)
	}
	if query.Has("kind") {
		request.Kind = query.Get("kind")
		if request.Kind != "task" && request.Kind != "entry" {
			return nil, "unsupported_submission_filter"
		}
	}
	if query.Has("state") {
		request.State = query.Get("state")
		valid := request.State == "pending_review" || request.State == "rejected"
		if request.Kind == "entry" {
			valid = valid || request.State == "approved" || request.State == "approval_revoked"
		} else {
			valid = valid || request.State == "draft" || request.State == "published" || request.State == "taken_down"
		}
		if !valid {
			return nil, "unsupported_submission_filter"
		}
	}
	if query.Has("category") {
		category := query.Get("category")
		if category == "" || !utf8.ValidString(category) || len(category) > maxCategoryBytes || strings.ContainsRune(category, 0) {
			return nil, "invalid_category"
		}
		request.Category = &category
	}
	return request, ""
}
func (h *Handler) listSubmissions(w http.ResponseWriter, r *http.Request) {
	request, reason := submissionListRequest(r.URL.RawQuery)
	if reason != "" {
		problem(w, 400, reason)
		return
	}
	operation := contract.ListOwnSubmissions
	if request.Kind == "entry" {
		operation = contract.ListOwnEntrySubmissions
	}
	actor, ok := h.contentActor(w, r, operation)
	if !ok {
		return
	}
	if request.Kind == "entry" {
		h.listEntrySubmissions(w, r, &pb.ListMyEntrySubmissionsRequest{ActorAssertion: actor, State: request.State, Category: request.Category, Limit: request.Limit, Cursor: request.Cursor})
		return
	}
	request.ActorAssertion = actor
	response, err := h.options.Content.ListMySubmissions(r.Context(), request)
	if err != nil {
		contentError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, item := range response.Items {
		value, err := taskJSON(item)
		if err != nil {
			problem(w, 500, "internal_error")
			return
		}
		items = append(items, value)
	}
	var cursor any
	if response.NextCursor != "" {
		cursor = response.NextCursor
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": cursor})
}

func (h *Handler) getDraft(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, contract.ReadOwnSubmission)
	if !ok {
		return
	}
	response, err := h.options.Content.GetMyTaskSubmission(r.Context(), &pb.GetMyTaskSubmissionRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId")})
	if err != nil {
		contentError(w, err)
		return
	}
	taskResponse(w, 200, response.Submission)
}
func (h *Handler) replaceDraft(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, contract.ReplaceDraft)
	if !ok {
		return
	}
	var body struct {
		ExpectedRevision int64           `json:"expectedRevision"`
		Content          json.RawMessage `json:"content"`
	}
	if !decode(w, r, &body) {
		return
	}
	input, ok := draftInput(body.Content)
	if !ok {
		problem(w, 400, "invalid_draft")
		return
	}
	assetActor := ""
	if hasFileArtifact(input) {
		var assetOK bool
		assetActor, assetOK = h.assetActor(w, r, contract.CheckAssetOwnership)
		if !assetOK {
			return
		}
	}
	response, err := h.options.Content.ReplaceTaskDraft(r.Context(), &pb.ReplaceTaskDraftRequest{ActorAssertion: actor, AssetActorAssertion: assetActor, TaskId: r.PathValue("taskId"), ExpectedRevision: body.ExpectedRevision, Content: input})
	if err != nil {
		contentError(w, err)
		return
	}
	taskResponse(w, 200, response.Submission)
}
func taskJSON(task *pb.TaskSubmission) (map[string]any, error) {
	if task == nil || task.Content == nil {
		return nil, errors.New("missing task submission")
	}
	data, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(task.Content)
	if err != nil {
		return nil, err
	}
	var review, reason any
	if task.ReviewId != nil {
		review = *task.ReviewId
	}
	if task.RejectionReason != nil {
		reason = *task.RejectionReason
	}
	return map[string]any{"kind": "task", "id": task.Id, "authorId": task.AuthorId, "revision": task.Revision, "state": task.State, "content": json.RawMessage(data), "reviewId": review, "rejectionReason": reason}, nil
}
func taskResponse(w http.ResponseWriter, code int, task *pb.TaskSubmission) {
	value, err := taskJSON(task)
	if err != nil {
		problem(w, 500, "internal_error")
		return
	}
	jsonResponse(w, code, value)
}
func contentError(w http.ResponseWriter, err error) {
	rpcErrorFor(w, err, "content_unavailable")
}
