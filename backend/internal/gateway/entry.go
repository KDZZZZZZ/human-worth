package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"google.golang.org/protobuf/encoding/protojson"
)

var entryCreateKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

// Decode exact HTTP names before protobuf decoding. encoding/json's struct
// decoder alone accepts duplicate and case-insensitive envelope fields.
func httpObject(raw json.RawMessage, required []string, optional ...string) (map[string]json.RawMessage, bool) {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, name := range required {
		allowed[name] = true
	}
	for _, name := range optional {
		allowed[name] = true
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, false
	}
	object := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || !allowed[name] || object[name] != nil {
			return nil, false
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, false
		}
		object[name] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || decoder.Decode(new(any)) != io.EOF {
		return nil, false
	}
	for _, name := range required {
		if object[name] == nil {
			return nil, false
		}
	}
	return object, true
}

func entryInput(raw json.RawMessage) (*pb.EntryInput, bool) {
	if _, ok := httpObject(raw, []string{"side", "title", "source", "artifacts", "permissions"}, "description", "agentConfiguration"); !ok || !httpDraftNames(raw) {
		return nil, false
	}
	input := &pb.EntryInput{}
	if protojson.Unmarshal(raw, input) != nil || (input.Side != "human" && input.Side != "agent") || strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.Source) == "" || input.Permissions == nil || input.Permissions.CloudUse == nil || len(input.Artifacts) == 0 || len(input.Artifacts) > 32 {
		return nil, false
	}
	if input.Side == "agent" {
		if input.AgentConfiguration == nil || strings.TrimSpace(input.AgentConfiguration.Model) == "" {
			return nil, false
		}
	} else if input.AgentConfiguration != nil {
		return nil, false
	}
	for _, artifact := range input.Artifacts {
		if artifact == nil || (artifact.Kind != "file" && artifact.Kind != "link" && artifact.Kind != "text") {
			return nil, false
		}
		if artifact.Kind == "file" && (artifact.GetAssetId() == "" || artifact.Url != nil || artifact.Text != nil) || artifact.Kind == "link" && (artifact.GetUrl() == "" || artifact.AssetId != nil || artifact.Text != nil) || artifact.Kind == "text" && (strings.TrimSpace(artifact.GetText()) == "" || artifact.AssetId != nil || artifact.Url != nil) {
			return nil, false
		}
	}
	return input, true
}

func (h *Handler) entryAssetActor(w http.ResponseWriter, r *http.Request, input *pb.EntryInput) (string, bool) {
	for _, artifact := range input.Artifacts {
		if artifact.Kind == "file" {
			return h.assetActor(w, r, contract.CheckAssetOwnership)
		}
	}
	return "", true
}

func (h *Handler) submitAdditionalEntry(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, contract.SubmitAdditionalEntry)
	if !ok || !noQuery(w, r) {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 || !entryCreateKey.MatchString(r.Header.Get("Idempotency-Key")) {
		problem(w, 400, "invalid_idempotency_key")
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	input, ok := entryInput(raw)
	if !ok {
		problem(w, 400, "invalid_entry")
		return
	}
	asset, ok := h.entryAssetActor(w, r, input)
	if !ok {
		return
	}
	out, err := h.options.Content.SubmitAdditionalEntry(r.Context(), &pb.SubmitAdditionalEntryRequest{ActorAssertion: actor, TaskId: r.PathValue("taskId"), IdempotencyKey: r.Header.Get("Idempotency-Key"), Content: input, AssetActorAssertion: asset})
	if err != nil {
		contentError(w, err)
		return
	}
	entryResponse(w, 201, out.GetSubmission())
}

func (h *Handler) getEntrySubmission(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, contract.ReadOwnEntrySubmission)
	if !ok || !noQuery(w, r) {
		return
	}
	out, err := h.options.Content.GetMyEntrySubmission(r.Context(), &pb.GetMyEntrySubmissionRequest{ActorAssertion: actor, EntryId: r.PathValue("entryId")})
	if err != nil {
		contentError(w, err)
		return
	}
	entryResponse(w, 200, out.GetSubmission())
}

func (h *Handler) resubmitRejectedEntry(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.contentActor(w, r, contract.ResubmitRejectedEntry)
	if !ok || !noQuery(w, r) {
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	body, ok := httpObject(raw, []string{"expectedRevision", "content"})
	if !ok {
		problem(w, 400, "invalid_request")
		return
	}
	var revision int64
	if json.Unmarshal(body["expectedRevision"], &revision) != nil || revision < 1 {
		problem(w, 400, "invalid_revision")
		return
	}
	input, ok := entryInput(body["content"])
	if !ok {
		problem(w, 400, "invalid_entry")
		return
	}
	asset, ok := h.entryAssetActor(w, r, input)
	if !ok {
		return
	}
	out, err := h.options.Content.ResubmitRejectedEntry(r.Context(), &pb.ResubmitRejectedEntryRequest{ActorAssertion: actor, EntryId: r.PathValue("entryId"), ExpectedRevision: revision, Content: input, AssetActorAssertion: asset})
	if err != nil {
		contentError(w, err)
		return
	}
	entryResponse(w, 200, out.GetSubmission())
}

func entryJSON(entry *pb.EntrySubmission) (map[string]any, error) {
	if entry == nil || entry.Content == nil || entry.CreatedAt.CheckValid() != nil || entry.UpdatedAt.CheckValid() != nil || entry.ApprovedAt != nil && entry.ApprovedAt.CheckValid() != nil {
		return nil, errors.New("invalid entry submission")
	}
	data, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(entry.Content)
	if err != nil {
		return nil, err
	}
	var review, reason, approved any
	if entry.ReviewId != "" {
		review = entry.ReviewId
	}
	if entry.RejectionReason != nil {
		reason = *entry.RejectionReason
	}
	if entry.ApprovedAt != nil {
		approved = entry.ApprovedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{"kind": "entry", "id": entry.Id, "taskId": entry.TaskId, "authorId": entry.AuthorId, "revision": entry.Revision, "origin": entry.Origin, "state": entry.State, "content": json.RawMessage(data), "reviewId": review, "rejectionReason": reason, "createdAt": entry.CreatedAt.AsTime().UTC().Format(time.RFC3339Nano), "updatedAt": entry.UpdatedAt.AsTime().UTC().Format(time.RFC3339Nano), "approvedAt": approved}, nil
}

func entryResponse(w http.ResponseWriter, code int, entry *pb.EntrySubmission) {
	value, err := entryJSON(entry)
	if err != nil {
		problem(w, 500, "internal_error")
		return
	}
	jsonResponse(w, code, value)
}

func (h *Handler) listEntrySubmissions(w http.ResponseWriter, r *http.Request, request *pb.ListMyEntrySubmissionsRequest) {
	out, err := h.options.Content.ListMyEntrySubmissions(r.Context(), request)
	if err != nil {
		contentError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(out.GetItems()))
	for _, item := range out.GetItems() {
		value, err := entryJSON(item)
		if err != nil {
			problem(w, 500, "internal_error")
			return
		}
		items = append(items, value)
	}
	var cursor any
	if out.GetNextCursor() != "" {
		cursor = out.GetNextCursor()
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": cursor})
}
