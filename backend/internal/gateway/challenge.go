package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// challengeRoutes 只注册管理员 HTTP 入口，内部领取、文件与预算接口不对公网开放。
func (h *Handler) challengeRoutes(mux *http.ServeMux) {
	for _, route := range []struct {
		pattern, name string
		handler       http.HandlerFunc
	}{
		{"POST /api/admin/challenge-runs", "startChallengeRun", h.startChallenge},
		{"GET /api/admin/challenge-runs", "listChallengeRuns", h.listChallenges},
		{"GET /api/admin/challenge-runs/{runId}", "getChallengeRun", h.getChallenge},
		{"POST /api/admin/challenge-runs/{runId}/cancel", "cancelChallengeRun", h.cancelChallenge},
		{"POST /api/admin/challenge-runs/{runId}/restart", "restartChallengeRun", h.restartChallenge},
		{"PUT /api/admin/challenge-runs/{runId}/candidates/{candidateId}/registration", "registerCandidate", h.registerChallenge},
	} {
		mux.HandleFunc(route.pattern, h.operation(route.name, route.handler))
	}
}
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// challengeJSON 显式输出公开字段；protobuf 的 int64 转成 OpenAPI 约定的 JSON 数字。
func challengeJSON(r *pb.Run) map[string]any {
	b, _ := protojson.MarshalOptions{EmitUnpopulated: true}.Marshal(r)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	for _, key := range []string{"parentRunId", "failureReason"} {
		out[key] = nullable(out[key].(string))
	}
	configuration := out["configuration"].(map[string]any)
	for _, role := range []string{"packer", "ranker", "executor"} {
		model := configuration[role].(map[string]any)
		if model["parameters"] == nil {
			model["parameters"] = map[string]any{}
		}
	}
	initial := configuration["initialTaskPackage"].(map[string]any)
	initial["taskRevision"] = r.Configuration.InitialTaskPackage.TaskRevision
	budget := configuration["budget"].(map[string]any)
	budget["amount"] = r.Configuration.Budget.Amount
	fit := out["rankerFit"].(map[string]any)
	if r.RankerFit.Score == nil {
		fit["score"] = nil
	}
	for _, item := range out["candidates"].([]any) {
		candidate := item.(map[string]any)
		candidate["entryId"] = nullable(candidate["entryId"].(string))
		candidate["reviewId"] = nullable(candidate["reviewId"].(string))
	}
	return out
}
func configJSON(w http.ResponseWriter, raw json.RawMessage) (*pb.RunConfiguration, bool) {
	v := &pb.RunConfiguration{}
	if len(raw) == 0 || (protojson.UnmarshalOptions{DiscardUnknown: false}).Unmarshal(raw, v) != nil {
		problem(w, 400, "invalid_configuration")
		return nil, false
	}
	return v, true
}
func idem(w http.ResponseWriter, r *http.Request) (string, bool) {
	if len(r.Header.Values("Idempotency-Key")) != 1 || r.Header.Get("Idempotency-Key") == "" {
		problem(w, 400, "idempotency_key_required")
		return "", false
	}
	return r.Header.Get("Idempotency-Key"), true
}
func (h *Handler) startChallenge(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.ChallengeService_StartRun_FullMethodName)
	if !ok {
		return
	}
	key, ok := idem(w, r)
	if !ok {
		return
	}
	var body struct {
		TaskID        string          `json:"taskId"`
		Configuration json.RawMessage `json:"configuration"`
	}
	if !decode(w, r, &body) {
		return
	}
	cfg, ok := configJSON(w, body.Configuration)
	if !ok {
		return
	}
	v, err := h.options.Challenge.StartRun(r.Context(), &pb.StartRunRequest{ActorAssertion: actor, TaskId: body.TaskID, Configuration: cfg, IdempotencyKey: key})
	if err != nil {
		rpcError(w, err)
		return
	}
	jsonResponse(w, 202, challengeJSON(v.Run))
}
func (h *Handler) getChallenge(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.ChallengeService_GetRun_FullMethodName)
	if !ok {
		return
	}
	v, err := h.options.Challenge.GetRun(r.Context(), &pb.GetRunRequest{ActorAssertion: actor, RunId: r.PathValue("runId")})
	if err != nil {
		rpcError(w, err)
		return
	}
	jsonResponse(w, 200, challengeJSON(v.Run))
}
func (h *Handler) listChallenges(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.ChallengeService_ListRuns_FullMethodName)
	if !ok {
		return
	}
	query := r.URL.Query()
	limit := 20
	for k, v := range query {
		if k != "limit" && k != "cursor" || len(v) != 1 {
			problem(w, 400, "invalid_pagination")
			return
		}
	}
	if query.Has("limit") {
		n, err := strconv.Atoi(query.Get("limit"))
		if err != nil || n < 1 || n > 100 {
			problem(w, 400, "invalid_pagination")
			return
		}
		limit = n
	}
	v, err := h.options.Challenge.ListRuns(r.Context(), &pb.ListRunsRequest{ActorAssertion: actor, Cursor: query.Get("cursor"), Limit: int32(limit)})
	if err != nil {
		rpcError(w, err)
		return
	}
	items := make([]any, 0, len(v.Items))
	for _, run := range v.Items {
		items = append(items, challengeJSON(run))
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)})
}
func (h *Handler) cancelChallenge(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.ChallengeService_CancelRun_FullMethodName)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decode(w, r, &body) {
		return
	}
	v, err := h.options.Challenge.CancelRun(r.Context(), &pb.CancelRunRequest{ActorAssertion: actor, RunId: r.PathValue("runId"), Reason: body.Reason})
	if err != nil {
		rpcError(w, err)
		return
	}
	code := 202
	if v.Run.Status == "cancelled" {
		code = 200
	}
	jsonResponse(w, code, challengeJSON(v.Run))
}
func (h *Handler) restartChallenge(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.ChallengeService_RestartRun_FullMethodName)
	if !ok {
		return
	}
	key, ok := idem(w, r)
	if !ok {
		return
	}
	var body struct {
		Configuration json.RawMessage `json:"configuration"`
	}
	if !decode(w, r, &body) {
		return
	}
	cfg, ok := configJSON(w, body.Configuration)
	if !ok {
		return
	}
	v, err := h.options.Challenge.RestartRun(r.Context(), &pb.RestartRunRequest{ActorAssertion: actor, ParentRunId: r.PathValue("runId"), Configuration: cfg, IdempotencyKey: key})
	if err != nil {
		rpcError(w, err)
		return
	}
	jsonResponse(w, 202, challengeJSON(v.Run))
}
func (h *Handler) registerChallenge(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.ChallengeService_RegisterCandidate_FullMethodName)
	if !ok {
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil || len(body) != 0 {
		problem(w, 400, "unexpected_body")
		return
	}
	v, err := h.options.Challenge.RegisterCandidate(r.Context(), &pb.RegisterCandidateRequest{ActorAssertion: actor, RunId: r.PathValue("runId"), CandidateId: r.PathValue("candidateId")})
	if err != nil {
		rpcError(w, err)
		return
	}
	receipt := v.Receipt
	code := 200
	if receipt.Created {
		code = 201
	}
	jsonResponse(w, code, map[string]any{"runId": receipt.RunId, "candidateId": receipt.CandidateId, "entryId": receipt.EntryId, "reviewId": nullable(receipt.ReviewId), "reviewState": receipt.ReviewState, "created": receipt.Created})
}
