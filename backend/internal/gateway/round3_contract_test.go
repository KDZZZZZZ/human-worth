package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	identity "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"google.golang.org/grpc"
)

type toolListIdentity struct{ identity.IdentityServiceClient }

func (toolListIdentity) ResolvePrincipal(context.Context, *identity.ResolvePrincipalRequest, ...grpc.CallOption) (*identity.ResolvePrincipalResponse, error) {
	return &identity.ResolvePrincipalResponse{ActorAssertion: "synthetic", Principal: &identity.Principal{AccountId: "account_synthetic", ClientKind: identity.ClientKind_CLIENT_KIND_MCP_READ}}, nil
}

func TestMCPToolListProtocolCachePolicy(t *testing.T) {
	for _, version := range mcpVersions {
		t.Run(version, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
			if version == "2026-07-28" {
				body = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
			}
			r := httptest.NewRequest("POST", "https://worth.example.test/api/mcp", strings.NewReader(body))
			for k, v := range map[string]string{"Authorization": "Bearer synthetic", "Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Origin": "https://worth.example.test", "MCP-Protocol-Version": version, "Mcp-Method": "tools/list"} {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			h := &Handler{client: toolListIdentity{}, options: Options{Origin: "https://worth.example.test"}}
			h.mcp(w, r)
			var result struct{ Result map[string]any }
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || len(result.Result["tools"].([]any)) != 5 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if version == "2026-07-28" {
				if result.Result["ttlMs"] != float64(0) || result.Result["cacheScope"] != "private" {
					t.Fatalf("authenticated tool discovery must not be cached: %s", w.Body)
				}
			} else if _, exists := result.Result["ttlMs"]; exists {
				t.Fatalf("modern cache policy leaked into legacy protocol: %s", w.Body)
			}
		})
	}
}
