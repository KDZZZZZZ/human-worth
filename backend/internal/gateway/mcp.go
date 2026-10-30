package gateway

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	discoverypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/discovery/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc/status"
)

// Stateless Streamable HTTP: the only stateful operation is the explicitly
// confirmed statistics read, committed by Voting for this stable account.
var mcpVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26"}

const mcpWarning = "只读取公开内容。view_task_statistics 必须由用户明确选择：调用前先说明会永久关闭该账号在该任务上的后续投票资格；更换 token、刷新列表或新增作品均不会恢复。普通读取不改变资格。"

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}
type mcpParams struct {
	Meta            map[string]json.RawMessage `json:"_meta"`
	Name            string                     `json:"name"`
	Arguments       json.RawMessage            `json:"arguments"`
	Cursor          string                     `json:"cursor"`
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]any             `json:"capabilities"`
	ClientInfo      map[string]any             `json:"clientInfo"`
	RequestID       json.RawMessage            `json:"requestId"`
	Reason          string                     `json:"reason"`
}

func mcpDecode(data []byte, out any) bool {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	return d.Decode(out) == nil && d.Decode(new(any)) == io.EOF
}
func mcpSupported(v string) bool {
	for _, x := range mcpVersions {
		if v == x {
			return true
		}
	}
	return false
}
func mcpHeader(v string) string {
	if strings.HasPrefix(v, "=?base64?") && strings.HasSuffix(v, "?=") {
		b, e := base64.StdEncoding.DecodeString(v[9 : len(v)-2])
		if e != nil {
			return ""
		}
		return string(b)
	}
	return v
}
func mcpError(w http.ResponseWriter, httpCode int, id json.RawMessage, code int, message string, data any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	e := map[string]any{"code": code, "message": message}
	if data != nil {
		e["data"] = data
	}
	jsonResponse(w, httpCode, map[string]any{"jsonrpc": "2.0", "id": id, "error": e})
}
func mcpResult(w http.ResponseWriter, id json.RawMessage, version string, result map[string]any) {
	if version == mcpVersions[0] {
		result["resultType"] = "complete"
		result["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": map[string]string{"name": "human-worth", "version": platform.BuildVersion()}}
	}
	jsonResponse(w, 200, map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
func (h *Handler) mcp(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		problem(w, 400, "invalid_request")
		return
	}
	if len(r.Header.Values("Origin")) > 1 || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.options.Origin) {
		problem(w, 403, "origin_forbidden")
		return
	}
	// MCP never borrows the browser's login or anonymous identity.
	if len(r.Header.Values("Authorization")) != 1 || len(r.Header.Values("Cookie")) != 0 {
		problem(w, 401, "mcp_token_required")
		return
	}
	kind, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || kind != "Bearer" || token == "" || strings.ContainsAny(token, " \t\r\n") {
		problem(w, 401, "mcp_token_required")
		return
	}
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		problem(w, 415, "json_required")
		return
	}
	accepted := map[string]bool{}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		m, p, e := mime.ParseMediaType(strings.TrimSpace(part))
		if e == nil && p["q"] != "0" {
			accepted[m] = true
		}
	}
	if !accepted["application/json"] || !accepted["text/event-stream"] {
		problem(w, 406, "mcp_accept_required")
		return
	}
	b, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, 413, "request_too_large")
		return
	}
	if !json.Valid(b) {
		mcpError(w, 400, nil, -32700, "Parse error", nil)
		return
	}
	var q mcpRequest
	if !mcpDecode(b, &q) || q.JSONRPC != "2.0" || q.Method == "" {
		mcpError(w, 400, nil, -32600, "Invalid Request", nil)
		return
	}
	if len(q.ID) > 0 {
		var s string
		var n json.Number
		if json.Unmarshal(q.ID, &s) != nil {
			if json.Unmarshal(q.ID, &n) != nil {
				mcpError(w, 400, nil, -32600, "Invalid request id", nil)
				return
			}
			if _, e := strconv.ParseInt(string(n), 10, 64); e != nil {
				mcpError(w, 400, nil, -32600, "Invalid request id", nil)
				return
			}
		}
		if string(q.ID) == "null" {
			mcpError(w, 400, nil, -32600, "Invalid request id", nil)
			return
		}
	}
	var p mcpParams
	if len(q.Params) > 0 && !mcpDecode(q.Params, &p) {
		mcpError(w, 400, q.ID, -32602, "Invalid params", nil)
		return
	}
	version := r.Header.Get("MCP-Protocol-Version")
	if version == "" {
		version = "2025-03-26"
	}
	if len(r.Header.Values("MCP-Protocol-Version")) > 1 || !mcpSupported(version) {
		mcpError(w, 400, q.ID, -32022, "Unsupported protocol version", map[string]any{"supportedVersions": mcpVersions})
		return
	}
	if version == mcpVersions[0] {
		var metaVersion string
		_ = json.Unmarshal(p.Meta["io.modelcontextprotocol/protocolVersion"], &metaVersion)
		var capabilities map[string]any
		if json.Unmarshal(p.Meta["io.modelcontextprotocol/clientCapabilities"], &capabilities) != nil || capabilities == nil {
			mcpError(w, 400, q.ID, -32602, "Client capabilities required", nil)
			return
		}
		if metaVersion != version || len(r.Header.Values("Mcp-Method")) != 1 || r.Header.Get("Mcp-Method") != q.Method || (q.Method == "tools/call" && (len(r.Header.Values("Mcp-Name")) != 1 || mcpHeader(r.Header.Get("Mcp-Name")) != p.Name)) {
			mcpError(w, 400, q.ID, -32020, "Header mismatch", nil)
			return
		}
	}
	// Authenticate discovery, ping and notifications too; revoked credentials
	// must not survive an earlier initialize request on another replica.
	op := contract.ReadTask
	if q.Method == "tools/call" {
		switch p.Name {
		case "list_tasks":
			op = contract.ListTasks
		case "get_task":
			op = contract.ReadTask
		case "list_entries":
			op = contract.ListApprovedEntries
		case "list_comments":
			op = contract.ListComments
		case "view_task_statistics":
			op = contract.ViewStatistics
		}
	}
	definition, _ := contract.LookupOperation(op)
	actor, err := h.client.ResolvePrincipal(r.Context(), &identitypb.ResolvePrincipalRequest{Audience: definition.Policy.Audience, FullMethod: definition.FullMethod, Credential: &identitypb.ResolvePrincipalRequest_McpToken{McpToken: token}})
	if err != nil {
		rpcError(w, err)
		return
	}
	if len(q.ID) == 0 {
		if q.Method == "notifications/initialized" || q.Method == "notifications/cancelled" {
			w.WriteHeader(202)
		} else {
			w.WriteHeader(400)
		}
		return
	}
	result := map[string]any{}
	switch q.Method {
	case "initialize":
		if version == mcpVersions[0] {
			mcpError(w, 404, q.ID, -32601, "Method not found", nil)
			return
		}
		selected := p.ProtocolVersion
		if !mcpSupported(selected) || selected == mcpVersions[0] {
			selected = "2025-11-25"
		}
		result = map[string]any{"protocolVersion": selected, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "human-worth", "version": platform.BuildVersion()}, "instructions": mcpWarning}
	case "server/discover":
		result = map[string]any{"supportedVersions": mcpVersions, "capabilities": map[string]any{"tools": map[string]any{}}, "instructions": mcpWarning}
	case "ping":
	case "tools/list":
		if p.Cursor != "" {
			mcpError(w, 400, q.ID, -32602, "Invalid cursor", nil)
			return
		}
		result["tools"] = mcpTools()
		if version == mcpVersions[0] {
			// Discovery authenticates every request; do not reuse it after revocation.
			result["ttlMs"] = 0
			result["cacheScope"] = "private"
		}
	case "tools/call":
		value, reason := h.mcpTool(r, actor.ActorAssertion, p)
		if reason != "" {
			result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": reason}}}
		} else {
			data, _ := json.Marshal(value)
			result = map[string]any{"content": []any{map[string]string{"type": "text", "text": string(data)}}, "structuredContent": value, "isError": false}
		}
	default:
		mcpError(w, 404, q.ID, -32601, "Method not found", nil)
		return
	}
	mcpResult(w, q.ID, version, result)
}

func mcpTools() []any {
	text := map[string]any{"type": "string", "minLength": 1}
	page := func() map[string]any {
		return map[string]any{"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}, "cursor": text}
	}
	makeTool := func(name, description string, props map[string]any, required []string, read bool) any {
		return map[string]any{"name": name, "description": description, "inputSchema": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}, "annotations": map[string]any{"readOnlyHint": read, "destructiveHint": !read, "idempotentHint": true, "openWorldHint": false}}
	}
	list := page()
	list["order"] = map[string]any{"enum": []string{"recommended", "hot"}}
	list["category"] = text
	list["q"] = map[string]any{"type": "string", "minLength": 1, "maxLength": 200}
	entries := page()
	entries["taskId"] = text
	return []any{makeTool("list_tasks", "分页读取已公开任务；不返回投票统计，也不改变资格。", list, []string{}, true), makeTool("get_task", "读取已公开任务和独立附件；不读取作品得票或改变资格。", map[string]any{"taskId": text}, []string{"taskId"}, true), makeTool("list_entries", "分页读取该任务全部过审作品；不包含投票统计。", entries, []string{"taskId"}, true), makeTool("list_comments", "分页读取公开评论。", entries, []string{"taskId"}, true), makeTool("view_task_statistics", mcpWarning, map[string]any{"taskId": text, "permanentlyCloseVoting": map[string]any{"type": "boolean", "const": true}}, []string{"taskId", "permanentlyCloseVoting"}, false)}
}

func (h *Handler) mcpTool(r *http.Request, assertion string, p mcpParams) (any, string) {
	// An explicit per-tool allowlist prevents hidden statistics flags and keeps
	// posting, voting and cloud administration out of MCP entirely.
	allowed := map[string]map[string]bool{"list_tasks": {"limit": true, "cursor": true, "order": true, "category": true, "q": true}, "get_task": {"taskId": true}, "list_entries": {"taskId": true, "limit": true, "cursor": true}, "list_comments": {"taskId": true, "limit": true, "cursor": true}, "view_task_statistics": {"taskId": true, "permanentlyCloseVoting": true}}
	fields, ok := allowed[p.Name]
	if !ok {
		return nil, "unknown_tool"
	}
	if len(p.Arguments) == 0 {
		p.Arguments = json.RawMessage("{}")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(p.Arguments, &raw) != nil || raw == nil {
		return nil, "invalid_arguments"
	}
	for key, value := range raw {
		if !fields[key] || string(value) == "null" {
			return nil, "invalid_arguments"
		}
	}
	var a struct {
		TaskID   string  `json:"taskId"`
		Limit    int32   `json:"limit"`
		Cursor   string  `json:"cursor"`
		Order    string  `json:"order"`
		Category *string `json:"category"`
		Query    *string `json:"q"`
		Confirm  bool    `json:"permanentlyCloseVoting"`
	}
	if !mcpDecode(p.Arguments, &a) || len(a.Cursor) > 2048 || a.Limit < 0 || a.Limit > 100 || (raw["limit"] != nil && a.Limit == 0) || (p.Name != "list_tasks" && a.TaskID == "") {
		return nil, "invalid_arguments"
	}
	ctx := r.Context()
	switch p.Name {
	case "list_tasks":
		if h.options.Discovery == nil {
			return nil, "discovery_unavailable"
		}
		v, e := h.options.Discovery.ListTasks(ctx, &discoverypb.ListTasksRequest{ActorAssertion: assertion, Order: a.Order, Category: a.Category, Limit: a.Limit, Cursor: a.Cursor, Query: a.Query})
		if e != nil {
			return nil, status.Convert(e).Message()
		}
		items := make([]any, 0, len(v.Items))
		for _, t := range v.Items {
			items = append(items, publicTaskJSON(t))
		}
		return map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)}, ""
	case "get_task":
		if h.options.Content == nil {
			return nil, "content_unavailable"
		}
		v, e := h.options.Content.GetTask(ctx, &contentpb.GetTaskRequest{ActorAssertion: assertion, TaskId: a.TaskID})
		if e != nil {
			return nil, status.Convert(e).Message()
		}
		return publicTaskJSON(v.Task), ""
	case "list_entries":
		if h.options.Content == nil {
			return nil, "content_unavailable"
		}
		v, e := h.options.Content.ListApprovedEntries(ctx, &contentpb.ListApprovedEntriesRequest{ActorAssertion: assertion, TaskId: a.TaskID, Limit: a.Limit, Cursor: a.Cursor})
		if e != nil {
			return nil, status.Convert(e).Message()
		}
		items := make([]any, 0, len(v.Items))
		for _, entry := range v.Items {
			items = append(items, approvedEntryJSON(entry))
		}
		return map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)}, ""
	case "list_comments":
		if h.options.Content == nil {
			return nil, "content_unavailable"
		}
		v, e := h.options.Content.ListComments(ctx, &contentpb.ListCommentsRequest{ActorAssertion: assertion, TaskId: a.TaskID, Limit: a.Limit, Cursor: a.Cursor})
		if e != nil {
			return nil, status.Convert(e).Message()
		}
		items := make([]any, 0, len(v.Items))
		for _, c := range v.Items {
			items = append(items, commentJSON(c))
		}
		return map[string]any{"items": items, "nextCursor": nullable(v.NextCursor)}, ""
	case "view_task_statistics":
		if !a.Confirm {
			return nil, "statistics_confirmation_required"
		}
		if h.options.Voting == nil {
			return nil, "voting_unavailable"
		}
		v, e := h.options.Voting.ViewTaskStatistics(ctx, &votingpb.ViewTaskStatisticsRequest{ActorAssertion: assertion, TaskId: a.TaskID, PermanentlyCloseVoting: true})
		if e != nil {
			return nil, status.Convert(e).Message()
		}
		return statisticsJSON(v.Statistics), ""
	}
	return nil, "unknown_tool"
}
