package gateway

import (
	"encoding/json"
	"net/http"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

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
				if k == "asset_id" || k == "cloud_use" || k == "agent_configuration" {
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
func (h *Handler) contentActor(w http.ResponseWriter, r *http.Request, method string) (string, bool) {
	if h.options.Content == nil {
		problem(w, 503, "content_unavailable")
		return "", false
	}
	return h.actorFor(w, r, "content", method)
}
func (h *Handler) createDraft(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, pb.ContentService_CreateTaskDraft_FullMethodName)
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
	response, err := h.options.Content.CreateTaskDraft(r.Context(), &pb.CreateTaskDraftRequest{ActorAssertion: actor, IdempotencyKey: r.Header.Get("Idempotency-Key"), Content: input})
	if err != nil {
		contentError(w, err)
		return
	}
	taskResponse(w, 201, response.Submission)
}
func (h *Handler) getDraft(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, pb.ContentService_GetMyTaskSubmission_FullMethodName)
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
	actor, ok := h.contentActor(w, r, pb.ContentService_ReplaceTaskDraft_FullMethodName)
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
	response, err := h.options.Content.ReplaceTaskDraft(r.Context(), &pb.ReplaceTaskDraftRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), ExpectedRevision: body.ExpectedRevision, Content: input})
	if err != nil {
		contentError(w, err)
		return
	}
	taskResponse(w, 200, response.Submission)
}
func taskResponse(w http.ResponseWriter, code int, task *pb.TaskSubmission) {
	data, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(task.Content)
	if err != nil {
		problem(w, 500, "internal_error")
		return
	}
	jsonResponse(w, code, map[string]any{"kind": "task", "id": task.Id, "authorId": task.AuthorId, "revision": task.Revision, "state": task.State, "content": json.RawMessage(data), "reviewId": nil, "rejectionReason": nil})
}
func contentError(w http.ResponseWriter, err error) {
	rpcErrorFor(w, err, "content_unavailable")
}
