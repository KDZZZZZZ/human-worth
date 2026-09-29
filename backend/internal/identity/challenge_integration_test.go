//go:build integration

package identity

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	challengepb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/repo/postgres"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 只替代 Challenge 的业务列表，鉴权仍经过真实 mTLS Identity 和 PostgreSQL。
type challengeListProbe struct {
	challengepb.ChallengeServiceClient
	identity pb.IdentityServiceClient
	account  string
}

func (p challengeListProbe) ListRuns(ctx context.Context, req *challengepb.ListRunsRequest, _ ...grpc.CallOption) (*challengepb.ListRunsResponse, error) {
	v, err := p.identity.VerifyActor(ctx, &pb.VerifyActorRequest{ActorAssertion: req.ActorAssertion, FullMethod: challengepb.ChallengeService_ListRuns_FullMethodName})
	if err != nil {
		return nil, err
	}
	if v.Principal.AccountId != p.account || v.Principal.Role != pb.Role_ROLE_ADMIN {
		return nil, status.Error(codes.PermissionDenied, "administrator identity mismatch")
	}
	return &challengepb.ListRunsResponse{}, nil
}

func TestChallengeIdentityAuthorization(t *testing.T) {
	l := newIdentityLab(t)
	_, initial := l.login(t, "challenge-admin", nil)
	admin := application.NewAdmin(postgres.New(l.db), randomToken)
	must(t, admin.ChangeAccount(t.Context(), "test-operator", dto.ChangeAccountInput{AccountID: initial.Account.Id, Role: "admin", State: "active", ExpectedVersion: 1}))
	session, me := l.login(t, "challenge-admin", nil)
	user, _ := l.login(t, "challenge-user", nil)
	headers := map[string]string{"Origin": l.origin, "X-CSRF-Token": me.CsrfToken, "Content-Type": "application/json"}
	created := l.request(t, 0, "POST", "/api/me/mcp-tokens", []*http.Cookie{session}, `{"name":"admin MCP","createRequestId":"challenge-auth"}`, headers)
	httpStatus(t, created, http.StatusCreated)
	var token struct{ Token string }
	readJSON(t, created, &token)
	challenge := l.client(t, 1, "challenge", l.pki)
	content := l.client(t, 1, "content", l.pki)

	for _, operation := range []struct {
		method string
		write  bool
	}{
		{challengepb.ChallengeService_StartRun_FullMethodName, true},
		{challengepb.ChallengeService_GetRun_FullMethodName, false},
		{challengepb.ChallengeService_ListRuns_FullMethodName, false},
		{challengepb.ChallengeService_GetRunSummary_FullMethodName, false},
		{challengepb.ChallengeService_CancelRun_FullMethodName, true},
		{challengepb.ChallengeService_RestartRun_FullMethodName, true},
		{challengepb.ChallengeService_RegisterCandidate_FullMethodName, true},
	} {
		t.Run(operation.method, func(t *testing.T) {
			req := &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: session.Value}, Audience: "challenge", FullMethod: operation.method}
			if operation.write {
				_, err := l.clients[0].ResolvePrincipal(t.Context(), req)
				grpcCode(t, err, codes.PermissionDenied)
				req.Origin, req.CsrfToken = l.origin, me.CsrfToken
			}
			actor, err := l.clients[0].ResolvePrincipal(t.Context(), req)
			must(t, err)
			verified, err := challenge.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: operation.method})
			must(t, err)
			if verified.Principal.AccountId != me.Account.Id || verified.Principal.Role != pb.Role_ROLE_ADMIN {
				t.Fatal("Challenge did not receive the administrator's stable identity")
			}
			_, err = content.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: operation.method})
			grpcCode(t, err, codes.PermissionDenied)
			req.Audience = "identity"
			_, err = l.clients[0].ResolvePrincipal(t.Context(), req)
			grpcCode(t, err, codes.PermissionDenied)
			req.Audience = "challenge"
			req.Credential = &pb.ResolvePrincipalRequest_McpToken{McpToken: token.Token}
			_, err = l.clients[0].ResolvePrincipal(t.Context(), req)
			grpcCode(t, err, codes.PermissionDenied)
			req.Credential = &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: user.Value}
			_, err = l.clients[0].ResolvePrincipal(t.Context(), req)
			grpcCode(t, err, codes.PermissionDenied)
			req.Credential = nil
			_, err = l.clients[0].ResolvePrincipal(t.Context(), req)
			grpcCode(t, err, codes.Unauthenticated)
		})
	}

	// HTTP 回归覆盖合并时易遗漏的 audience=challenge；不能由假的 Identity 放行。
	handler, err := gateway.New(l.clients[0], gateway.Options{Origin: l.origin, Challenge: challengeListProbe{identity: challenge, account: me.Account.Id}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	must(t, err)
	web := httptest.NewTLSServer(handler)
	t.Cleanup(web.Close)
	origin, err := url.Parse(l.origin)
	must(t, err)
	for _, access := range []struct {
		cookie *http.Cookie
		status int
	}{{session, http.StatusOK}, {user, http.StatusForbidden}, {nil, http.StatusUnauthorized}} {
		req, err := http.NewRequestWithContext(t.Context(), "GET", web.URL+"/api/admin/challenge-runs", nil)
		must(t, err)
		req.Host = origin.Host
		if access.cookie != nil {
			req.AddCookie(access.cookie)
		}
		response, err := web.Client().Do(req)
		must(t, err)
		httpStatus(t, response, access.status)
		response.Body.Close()
	}

	actor, err := l.clients[0].ResolvePrincipal(t.Context(), &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: session.Value}, Audience: "challenge", FullMethod: challengepb.ChallengeService_ListRuns_FullMethodName})
	must(t, err)
	httpStatus(t, l.request(t, 0, "POST", "/api/auth/logout", []*http.Cookie{session}, "", headers), http.StatusNoContent)
	_, err = challenge.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: challengepb.ChallengeService_ListRuns_FullMethodName})
	grpcCode(t, err, codes.Unauthenticated)
}
