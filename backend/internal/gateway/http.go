package gateway

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const SessionCookie = "__Host-human-worth-session"
const FlowCookie = "__Host-human-worth-oauth"

//go:embed web/*
var web embed.FS

type Options struct {
	Content        contentpb.ContentServiceClient
	Origin         string
	LogoPath       string
	TrustedProxies []netip.Prefix
	Logger         *slog.Logger
	Registry       *prometheus.Registry
	Ready          func(context.Context) error
}
type Handler struct {
	client   pb.IdentityServiceClient
	options  Options
	host     string
	mu       sync.Mutex
	window   time.Time
	starts   int
	inflight chan struct{}
	latency  *prometheus.HistogramVec
}

func New(client pb.IdentityServiceClient, options Options) (http.Handler, error) {
	parsed, err := url.Parse(options.Origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid HTTPS public origin")
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	h := &Handler{client: client, options: options, host: parsed.Host, inflight: make(chan struct{}, 64)}
	if options.Registry != nil {
		h.latency = prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "human_worth_http_duration_seconds", Help: "API request duration", Buckets: []float64{.01, .05, .1, .25, .5, 1, 2, 5, 15}}, []string{"operation", "status"})
		options.Registry.MustRegister(h.latency)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if options.Ready != nil && options.Ready(ctx) != nil {
			problem(w, 503, "identity_unavailable")
			return
		}
		jsonResponse(w, 200, map[string]string{"status": "ok", "service": "human-worth", "stage": "identity", "revision": platform.BuildVersion()})
	})
	mux.HandleFunc("GET /{$}", h.page)
	if options.LogoPath != "" {
		mux.HandleFunc("GET /brand/logo.png", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, options.LogoPath)
		})
	}
	mux.HandleFunc("GET /account.js", func(w http.ResponseWriter, r *http.Request) {
		content, _ := web.ReadFile("web/account.js")
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Write(content)
	})
	for _, route := range []struct {
		method, path, operation string
		handler                 http.HandlerFunc
	}{
		{"POST", "/api/tasks", "createTaskDraft", h.createDraft},
		{"GET", "/api/me/tasks/{taskId}", "getMyTaskSubmission", h.getDraft},
		{"PUT", "/api/me/tasks/{taskId}", "replaceTaskDraft", h.replaceDraft},
		{"GET", "/api/auth/google", "startGoogleLogin", h.start}, {"GET", "/api/auth/google/callback", "completeGoogleLogin", h.callback},
		{"GET", "/api/me", "getCurrentSession", h.me}, {"POST", "/api/auth/logout", "logoutCurrentSession", h.logout},
		{"POST", "/api/me/mcp-tokens", "createMcpToken", h.createToken}, {"GET", "/api/me/mcp-tokens", "listMyMcpTokens", h.listTokens},
		{"DELETE", "/api/me/mcp-tokens/{credentialId}", "revokeMcpToken", h.revokeToken},
	} {
		mux.HandleFunc(route.method+" "+route.path, h.operation(route.operation, route.handler))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		if len(r.RequestURI) > 8192 {
			problem(w, 414, "invalid_request")
			return
		}
		if !h.secure(r) {
			problem(w, 400, "https_required")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
func (h *Handler) secure(r *http.Request) bool {
	if r.Host != h.host {
		return false
	}
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	if len(r.Header.Values("X-Forwarded-Proto")) != 1 || r.Header.Get("X-Forwarded-Proto") != "https" {
		return false
	}
	for _, network := range h.options.TrustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
func (h *Handler) operation(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		began := time.Now()
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
		ctx, span := otel.Tracer("human-worth.gateway").Start(ctx, name, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		recorder := &responseStatus{ResponseWriter: w, code: 200}
		w = recorder
		defer func() {
			if h.latency != nil {
				h.latency.WithLabelValues(name, strconv.Itoa(recorder.code)).Observe(time.Since(began).Seconds())
			}
			span.SetAttributes(attribute.Int("http.response.status_code", recorder.code))
			h.options.Logger.Info("http_request", "operation", name, "request_id", w.Header().Get("X-Request-ID"), "status", recorder.code, "duration_ms", time.Since(began).Milliseconds(), "trace_id", span.SpanContext().TraceID().String())
		}()
		select {
		case h.inflight <- struct{}{}:
			defer func() { <-h.inflight }()
		default:
			problem(w, 429, "too_many_requests")
			return
		}
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			problem(w, 503, "identity_unavailable")
			return
		}
		requestID := base64.RawURLEncoding.EncodeToString(id)
		w.Header().Set("X-Request-ID", requestID)
		ctx = metadata.AppendToOutgoingContext(ctx, "x-request-id", requestID)
		timeout := 2 * time.Second
		if name == "completeGoogleLogin" {
			timeout = 15 * time.Second
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		r = r.WithContext(ctx)
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
		next(w, r)
	}
}

type responseStatus struct {
	http.ResponseWriter
	code int
}

func (w *responseStatus) WriteHeader(code int) { w.code = code; w.ResponseWriter.WriteHeader(code) }
func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	content, _ := web.ReadFile("web/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}
func problem(w http.ResponseWriter, code int, reason string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"type": "about:blank", "title": http.StatusText(code), "status": code, "code": reason, "detail": http.StatusText(code)})
}
func rpcError(w http.ResponseWriter, err error) { rpcErrorFor(w, err, "identity_unavailable") }
func rpcErrorFor(w http.ResponseWriter, err error, unavailable string) {
	s := status.Convert(err)
	code := 503
	reason := s.Message()
	switch s.Code() {
	case codes.InvalidArgument:
		code = 400
	case codes.Unauthenticated:
		code = 401
	case codes.PermissionDenied:
		code = 403
	case codes.NotFound:
		code = 404
	case codes.AlreadyExists, codes.Aborted, codes.FailedPrecondition:
		code = 409
	case codes.ResourceExhausted:
		code = 429
	default:
		reason = unavailable
	}
	// Server messages are stable reason identifiers, never arbitrary dependency text.
	for _, c := range reason {
		if !(c >= 'a' && c <= 'z' || c == '_') {
			reason = unavailable
			break
		}
	}
	if len(reason) > 80 || reason == "" {
		reason = unavailable
	}
	problem(w, code, reason)
}
func cookie(r *http.Request, name string) (string, error) {
	value := ""
	count := 0
	for _, c := range r.Cookies() {
		if c.Name == name {
			count++
			value = c.Value
		}
	}
	if count > 1 || len(value) > 512 {
		return "", errors.New("invalid cookie")
	}
	return value, nil
}
func putCookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func hasAuthorization(r *http.Request) bool { return len(r.Header.Values("Authorization")) != 0 }
func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	if hasAuthorization(r) {
		problem(w, 400, "invalid_request")
		return
	}
	h.mu.Lock()
	if time.Since(h.window) >= time.Minute {
		h.window = time.Now()
		h.starts = 0
	}
	h.starts++
	limited := h.starts > 60
	h.mu.Unlock()
	if limited {
		w.Header().Set("Retry-After", "60")
		problem(w, 429, "too_many_requests")
		return
	}
	previous, err := cookie(r, FlowCookie)
	if err != nil {
		problem(w, 400, "invalid_request")
		return
	}
	response, err := h.client.StartGoogleLogin(r.Context(), &pb.StartGoogleLoginRequest{PreviousFlowCookie: previous})
	if err != nil {
		rpcError(w, err)
		return
	}
	putCookie(w, FlowCookie, response.FlowCookie, int(response.MaxAgeSeconds))
	http.Redirect(w, r, response.AuthorizationUrl, 302)
}
func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if hasAuthorization(r) {
		problem(w, 400, "invalid_request")
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["state"]) != 1 || query.Get("state") == "" || len(query["code"])+len(query["error"]) != 1 {
		problem(w, 400, "invalid_request")
		return
	}
	flow, err := cookie(r, FlowCookie)
	if err != nil {
		problem(w, 400, "invalid_request")
		return
	}
	old, err := cookie(r, SessionCookie)
	if err != nil {
		problem(w, 400, "invalid_request")
		return
	}
	request := &pb.CompleteGoogleLoginRequest{FlowCookie: flow, State: query.Get("state"), PreviousSessionCookie: old}
	if len(query["code"]) == 1 {
		request.Outcome = &pb.CompleteGoogleLoginRequest_Code{Code: query.Get("code")}
	} else {
		request.Outcome = &pb.CompleteGoogleLoginRequest_ProviderError{ProviderError: query.Get("error")}
	}
	response, err := h.client.CompleteGoogleLogin(r.Context(), request)
	if err != nil {
		if flow != "" {
			putCookie(w, FlowCookie, "", -1)
		}
		rpcError(w, err)
		return
	}
	putCookie(w, FlowCookie, "", -1)
	if response.SessionCookie != "" {
		putCookie(w, SessionCookie, response.SessionCookie, int(response.MaxAgeSeconds))
	}
	if response.RedirectPath != "/" && response.RedirectPath != "/?login=cancelled" {
		problem(w, 503, "identity_unavailable")
		return
	}
	http.Redirect(w, r, response.RedirectPath, 303)
}
func (h *Handler) actor(w http.ResponseWriter, r *http.Request, method string) (string, bool) {
	return h.actorFor(w, r, "identity", method)
}
func (h *Handler) actorFor(w http.ResponseWriter, r *http.Request, audience, method string) (string, bool) {
	session, err := cookie(r, SessionCookie)
	if err != nil {
		problem(w, 400, "invalid_request")
		return "", false
	}
	headers := r.Header.Values("Authorization")
	if len(headers) > 1 || (session != "" && len(headers) != 0) {
		problem(w, 400, "invalid_request")
		return "", false
	}
	if len(r.Header.Values("Origin")) > 1 || len(r.Header.Values("X-CSRF-Token")) > 1 {
		problem(w, 400, "invalid_request")
		return "", false
	}
	request := &pb.ResolvePrincipalRequest{Audience: audience, FullMethod: method, Origin: r.Header.Get("Origin"), CsrfToken: r.Header.Get("X-CSRF-Token")}
	if len(headers) == 1 {
		kind, value, ok := strings.Cut(headers[0], " ")
		if !ok || kind != "Bearer" || value == "" || strings.ContainsAny(value, " \t\r\n") {
			problem(w, 401, "unauthenticated")
			return "", false
		}
		request.Credential = &pb.ResolvePrincipalRequest_McpToken{McpToken: value}
	} else if session != "" {
		request.Credential = &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: session}
	}
	response, err := h.client.ResolvePrincipal(r.Context(), request)
	if err != nil {
		rpcError(w, err)
		return "", false
	}
	return response.ActorAssertion, true
}
func jsonResponse(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(value)
}
func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.IdentityService_GetCurrentSession_FullMethodName)
	if !ok {
		return
	}
	response, err := h.client.GetCurrentSession(r.Context(), &pb.GetCurrentSessionRequest{ActorAssertion: actor})
	if err != nil {
		rpcError(w, err)
		return
	}
	role := "user"
	if response.Role == pb.Role_ROLE_ADMIN {
		role = "admin"
	}
	jsonResponse(w, 200, map[string]any{"account": map[string]string{"id": response.Account.Id, "displayName": response.Account.DisplayName}, "role": role, "csrfToken": response.CsrfToken})
}
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.IdentityService_LogoutCurrentSession_FullMethodName)
	if !ok {
		return
	}
	_, err := h.client.LogoutCurrentSession(r.Context(), &pb.LogoutCurrentSessionRequest{ActorAssertion: actor})
	if err != nil {
		rpcError(w, err)
		return
	}
	putCookie(w, SessionCookie, "", -1)
	w.WriteHeader(204)
}
func decode(w http.ResponseWriter, r *http.Request, value any) bool {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		problem(w, 415, "unsupported_media_type")
		return false
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			problem(w, 413, "request_too_large")
		} else {
			problem(w, 400, "invalid_request")
		}
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		problem(w, 400, "invalid_request")
		return false
	}
	return true
}
func (h *Handler) createToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.IdentityService_CreateMcpToken_FullMethodName)
	if !ok {
		return
	}
	var body struct {
		Name      string `json:"name"`
		RequestID string `json:"createRequestId"`
	}
	if !decode(w, r, &body) {
		return
	}
	response, err := h.client.CreateMcpToken(r.Context(), &pb.CreateMcpTokenRequest{ActorAssertion: actor, Name: body.Name, CreateRequestId: body.RequestID})
	if err != nil {
		rpcError(w, err)
		return
	}
	jsonResponse(w, 201, map[string]any{"credential": tokenJSON(response.Credential), "token": response.Token})
}
func tokenJSON(token *pb.McpToken) map[string]any {
	state := "active"
	if token.State == pb.CredentialState_CREDENTIAL_STATE_REVOKED {
		state = "revoked"
	} else if token.State == pb.CredentialState_CREDENTIAL_STATE_EXPIRED {
		state = "expired"
	}
	var revoked any
	if token.RevokedAt != nil {
		revoked = token.RevokedAt.AsTime().UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{"id": token.Id, "name": token.Name, "createRequestId": token.CreateRequestId, "state": state, "createdAt": token.CreatedAt.AsTime().UTC().Format(time.RFC3339Nano), "expiresAt": token.ExpiresAt.AsTime().UTC().Format(time.RFC3339Nano), "revokedAt": revoked}
}
func (h *Handler) listTokens(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.IdentityService_ListMyMcpTokens_FullMethodName)
	if !ok {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["cursor"]) > 1 || len(query["limit"]) > 1 {
		problem(w, 400, "invalid_request")
		return
	}
	limit := 20
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			problem(w, 400, "invalid_request")
			return
		}
	}
	response, err := h.client.ListMyMcpTokens(r.Context(), &pb.ListMyMcpTokensRequest{ActorAssertion: actor, Cursor: query.Get("cursor"), Limit: int32(limit)})
	if err != nil {
		rpcError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, token := range response.Items {
		items = append(items, tokenJSON(token))
	}
	var cursor any
	if response.NextCursor != "" {
		cursor = response.NextCursor
	}
	jsonResponse(w, 200, map[string]any{"items": items, "nextCursor": cursor})
}
func (h *Handler) revokeToken(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.actor(w, r, pb.IdentityService_RevokeMcpToken_FullMethodName)
	if !ok {
		return
	}
	_, err := h.client.RevokeMcpToken(r.Context(), &pb.RevokeMcpTokenRequest{ActorAssertion: actor, CredentialId: r.PathValue("credentialId")})
	if err != nil {
		rpcError(w, err)
		return
	}
	w.WriteHeader(204)
}
