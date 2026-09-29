package grpc

import (
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"testing"
)

func TestCredentialPresenceSurvivesMapping(t *testing.T) {
	if in := resolveInput(&pb.ResolvePrincipalRequest{}); in.SessionCookie != nil || in.MCPToken != nil {
		t.Fatal("anonymous acquired credentials")
	}
	web := resolveInput(&pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: ""}})
	if web.SessionCookie == nil || *web.SessionCookie != "" || web.MCPToken != nil {
		t.Fatal("explicit empty cookie became anonymous")
	}
	mcp := resolveInput(&pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_McpToken{McpToken: ""}})
	if mcp.MCPToken == nil || *mcp.MCPToken != "" || mcp.SessionCookie != nil {
		t.Fatal("explicit empty token became anonymous")
	}
	code := completeLoginInput(&pb.CompleteGoogleLoginRequest{Outcome: &pb.CompleteGoogleLoginRequest_Code{Code: ""}})
	if code.Code == nil || code.ProviderError != nil {
		t.Fatal("code presence lost")
	}
	rejected := completeLoginInput(&pb.CompleteGoogleLoginRequest{Outcome: &pb.CompleteGoogleLoginRequest_ProviderError{ProviderError: "access_denied"}})
	if rejected.ProviderError == nil || *rejected.ProviderError != "access_denied" || rejected.Code != nil {
		t.Fatal("provider error became code")
	}
}
