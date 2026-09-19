//go:build integration

package identity

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

func TestIdentityHTTPAndCredentials(t *testing.T) {
	l := newIdentityLab(t)
	httpStatus(t, l.request(t, 0, "GET", "/api/me", nil, "", map[string]string{"X-Account-ID": "forged", "X-Role": "admin"}), 401)
	session, me := l.login(t, "person-one", nil)
	if me.Account.Id == "" || me.CsrfToken == "" || me.Role != pb.Role_ROLE_USER {
		t.Fatal("missing account/CSRF or unsafe initial role")
	}
	if me.Account.DisplayName != "测试用户 <script>" {
		t.Fatal("display name not preserved as data")
	}
	if session.MaxAge != 86400 {
		t.Fatal("session lifetime contract")
	}
	cookies := []*http.Cookie{session}
	headers := map[string]string{"Origin": l.origin, "X-CSRF-Token": me.CsrfToken, "Content-Type": "application/json"}

	t.Run("CSRF and credential channels", func(t *testing.T) {
		for _, h := range []map[string]string{nil, {"Origin": l.origin}, {"Origin": "https://evil.test", "X-CSRF-Token": me.CsrfToken}} {
			httpStatus(t, l.request(t, 1, "POST", "/api/auth/logout", cookies, "", h), 403)
		}
		httpStatus(t, l.request(t, 1, "GET", "/api/me", cookies, "", map[string]string{"Authorization": "Bearer other"}), 400)
		httpStatus(t, l.request(t, 1, "GET", "/api/me", []*http.Cookie{session, session}, "", nil), 400)
		httpStatus(t, l.request(t, 1, "GET", "/api/me", cookies, "", nil), 200)
	})

	var created struct {
		Token      string `json:"token"`
		Credential struct {
			ID      string `json:"id"`
			Request string `json:"createRequestId"`
		} `json:"credential"`
	}
	createBody := `{"name":"学习用 MCP","createRequestId":"request-one"}`
	response := l.request(t, 0, "POST", "/api/me/mcp-tokens", cookies, createBody, headers)
	httpStatus(t, response, 201)
	readJSON(t, response, &created)
	if created.Token == "" || created.Credential.ID == "" || created.Credential.Request != "request-one" {
		t.Fatal("missing one-time credential")
	}
	httpStatus(t, l.request(t, 1, "POST", "/api/me/mcp-tokens", cookies, createBody, headers), 409)
	for i := 0; i < 2; i++ {
		r := l.request(t, i, "POST", "/api/me/mcp-tokens", cookies, `{"name":"extra","createRequestId":"`+randomToken()+`"}`, headers)
		httpStatus(t, r, 201)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		r := l.request(t, 1, "GET", "/api/me/mcp-tokens?limit=1&cursor="+url.QueryEscape(cursor), cookies, "", nil)
		httpStatus(t, r, 200)
		body, err := io.ReadAll(r.Body)
		must(t, err)
		r.Body.Close()
		if bytes.Contains(body, []byte(created.Token)) || bytes.Contains(body, []byte(`"token"`)) {
			t.Fatal("list exposed credential secret")
		}
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
			Next *string `json:"nextCursor"`
		}
		must(t, json.Unmarshal(body, &page))
		if len(page.Items) != 1 || seen[page.Items[0].ID] {
			t.Fatal("pagination omitted or duplicated credentials")
		}
		seen[page.Items[0].ID] = true
		if page.Next == nil {
			break
		}
		cursor = *page.Next
	}
	if len(seen) != 3 {
		t.Fatal("pagination did not return all credentials")
	}
	var hash []byte
	var csrf *string
	must(t, l.db.QueryRow(t.Context(), `SELECT token_hash,csrf_cipher FROM identity.credentials WHERE id=$1`, created.Credential.ID).Scan(&hash, &csrf))
	if !bytes.Equal(hash, digest(created.Token)) || csrf != nil {
		t.Fatal("MCP credential storage is not hash-only")
	}
	var uncleared int
	must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.login_flows WHERE status='succeeded' AND verifier_cipher IS NOT NULL`).Scan(&uncleared))
	if uncleared != 0 {
		t.Fatal("PKCE verifier retained after completion")
	}

	resolve := func(method, audience string) (*pb.ResolvePrincipalResponse, error) {
		return l.clients[0].ResolvePrincipal(t.Context(), &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_McpToken{McpToken: created.Token}, Audience: audience, FullMethod: method})
	}
	readMethod := "/humanworth.content.v1.ContentService/GetTask"
	actor, err := resolve(readMethod, "content")
	must(t, err)
	if actor.Principal.AccountId != me.Account.Id || actor.Principal.ClientKind != pb.ClientKind_CLIENT_KIND_MCP_READ {
		t.Fatal("MCP identity diverged from web account")
	}
	content := l.client(t, 1, "content", l.pki)
	verified, err := content.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: readMethod})
	must(t, err)
	if verified.Principal.AccountId != me.Account.Id {
		t.Fatal("cross-service account mismatch")
	}
	for method, audience := range map[string]string{pb.IdentityService_GetCurrentSession_FullMethodName: "identity", pb.IdentityService_CreateMcpToken_FullMethodName: "identity", "/humanworth.voting.v1.VotingService/CastVote": "voting", "/humanworth.challenge.v1.ChallengeService/StartRun": "challenge"} {
		_, err = resolve(method, audience)
		grpcCode(t, err, codes.PermissionDenied)
	}
	_, err = resolve("/humanworth.voting.v1.VotingService/ViewTaskStatistics", "voting")
	must(t, err) // Identity only; Voting's durable eligibility is a separate integration.
	httpStatus(t, l.request(t, 0, "GET", "/api/me", nil, "", map[string]string{"Authorization": "Bearer " + created.Token}), 403)
	_, err = l.clients[0].ResolvePrincipal(t.Context(), &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: created.Token}, Audience: "identity", FullMethod: pb.IdentityService_GetCurrentSession_FullMethodName})
	grpcCode(t, err, codes.Unauthenticated)

	other, otherMe := l.login(t, "different-subject-same-email", nil)
	if otherMe.Account.Id == me.Account.Id {
		t.Fatal("accounts were linked by email")
	}
	otherHeaders := map[string]string{"Origin": l.origin, "X-CSRF-Token": otherMe.CsrfToken}
	httpStatus(t, l.request(t, 1, "DELETE", "/api/me/mcp-tokens/"+created.Credential.ID, []*http.Cookie{other}, "", otherHeaders), 404)
	for i := 0; i < 2; i++ {
		httpStatus(t, l.request(t, i, "DELETE", "/api/me/mcp-tokens/"+created.Credential.ID, cookies, "", headers), 204)
	}
	_, err = resolve(readMethod, "content")
	grpcCode(t, err, codes.Unauthenticated)
	_, err = content.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: readMethod})
	grpcCode(t, err, codes.Unauthenticated)
	var audits int
	must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.audit_events WHERE target_id=$1 AND action='mcp_revoked'`, created.Credential.ID).Scan(&audits))
	if audits != 1 {
		t.Fatal("revoke not idempotent")
	}

	webActor, err := l.clients[0].ResolvePrincipal(t.Context(), &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: session.Value}, Audience: "identity", FullMethod: pb.IdentityService_GetCurrentSession_FullMethodName})
	must(t, err)
	response = l.request(t, 1, "POST", "/api/auth/logout", cookies, "", headers)
	httpStatus(t, response, 204)
	if findCookie(t, response, gateway.SessionCookie).MaxAge != -1 {
		t.Fatal("logout did not clear browser cookie")
	}
	httpStatus(t, l.request(t, 0, "GET", "/api/me", cookies, "", nil), 401)
	_, err = l.clients[1].GetCurrentSession(t.Context(), &pb.GetCurrentSessionRequest{ActorAssertion: webActor.ActorAssertion})
	grpcCode(t, err, codes.Unauthenticated)
	renewed, again := l.login(t, "person-one", session)
	if again.Account.Id != me.Account.Id {
		t.Fatal("login changed stable account")
	}

	t.Run("role changes and disable invalidate existing credentials", func(t *testing.T) {
		admin := &Server{DB: l.db}
		must(t, admin.ChangeAccount(t.Context(), "test-operator", me.Account.Id, "admin", "active", 1))
		httpStatus(t, l.request(t, 1, "GET", "/api/me", []*http.Cookie{renewed}, "", nil), 401)
		newSession, newMe := l.login(t, "person-one", nil)
		if newMe.Role != pb.Role_ROLE_ADMIN {
			t.Fatal("role not loaded from authoritative account")
		}
		must(t, admin.ChangeAccount(t.Context(), "test-operator", me.Account.Id, "user", "disabled", 2))
		httpStatus(t, l.request(t, 0, "GET", "/api/me", []*http.Cookie{newSession}, "", nil), 401)
		flow, err := l.clients[0].StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
		must(t, err)
		u, _ := url.Parse(flow.AuthorizationUrl)
		code := l.provider.issue(t, flow.AuthorizationUrl, "person-one", nil)
		_, err = l.clients[1].CompleteGoogleLogin(t.Context(), &pb.CompleteGoogleLoginRequest{FlowCookie: flow.FlowCookie, State: u.Query().Get("state"), Outcome: &pb.CompleteGoogleLoginRequest_Code{Code: code}})
		grpcCode(t, err, codes.PermissionDenied)
		must(t, admin.ChangeAccount(t.Context(), "test-operator", me.Account.Id, "user", "active", 3))
		httpStatus(t, l.request(t, 0, "GET", "/api/me", []*http.Cookie{newSession}, "", nil), 401)
		_, restored := l.login(t, "person-one", nil)
		if restored.Account.Id != me.Account.Id {
			t.Fatal("re-enable replaced account")
		}
		grpcCode(t, admin.ChangeAccount(t.Context(), "test-operator", me.Account.Id, "admin", "active", 3), codes.Aborted)
	})

	t.Run("runtime database role cannot cross boundaries", func(t *testing.T) {
		for _, sql := range []string{`SELECT * FROM unrelated.private_data`, `CREATE TABLE identity.forbidden(id int)`, `UPDATE identity.accounts SET role='admin'`, `DELETE FROM identity.audit_events`, `UPDATE identity.schema_migrations SET checksum='forged'`} {
			if _, err := l.runtimeDB.Exec(t.Context(), sql); err == nil {
				t.Fatal("runtime database capability exceeded: " + sql)
			}
		}
	})
}
func grpcCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("gRPC code=%s want=%s error=%v", status.Code(err), want, err)
	}
}

func TestOAuthConcurrencyAndRejection(t *testing.T) {
	l := newIdentityLab(t)
	newFlow := func(t *testing.T, subject string, change func(*oidcCode)) (*pb.StartGoogleLoginResponse, *pb.CompleteGoogleLoginRequest) {
		t.Helper()
		f, err := l.clients[0].StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
		must(t, err)
		u, _ := url.Parse(f.AuthorizationUrl)
		code := l.provider.issue(t, f.AuthorizationUrl, subject, change)
		return f, &pb.CompleteGoogleLoginRequest{FlowCookie: f.FlowCookie, State: u.Query().Get("state"), Outcome: &pb.CompleteGoogleLoginRequest_Code{Code: code}}
	}

	t.Run("duplicate callbacks exchange once across replicas", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		_, req := newFlow(t, "once", func(c *oidcCode) { c.entered = entered; c.release = release })
		before := l.provider.exchanges.Load()
		done := make(chan error, 1)
		go func() { _, err := l.clients[0].CompleteGoogleLogin(t.Context(), req); done <- err }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("exchange did not start")
		}
		_, err := l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.InvalidArgument)
		release <- struct{}{}
		must(t, <-done)
		_, err = l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.InvalidArgument)
		if l.provider.exchanges.Load()-before != 1 {
			t.Fatal("authorization code exchanged more than once")
		}
	})
	t.Run("replaced in-flight callback cannot create session", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		flow, req := newFlow(t, "cancelled-inflight", func(c *oidcCode) { c.entered = entered; c.release = release })
		done := make(chan error, 1)
		go func() { _, err := l.clients[0].CompleteGoogleLogin(t.Context(), req); done <- err }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("exchange did not start")
		}
		_, err := l.clients[1].StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{PreviousFlowCookie: flow.FlowCookie})
		must(t, err)
		release <- struct{}{}
		grpcCode(t, <-done, codes.InvalidArgument)
		var count int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.external_identities WHERE subject='cancelled-inflight'`).Scan(&count))
		if count != 0 {
			t.Fatal("late callback created account")
		}
	})
	t.Run("concurrent first login keeps one stable account", func(t *testing.T) {
		var requests [2]*pb.CompleteGoogleLoginRequest
		for i := range requests {
			_, requests[i] = newFlow(t, "concurrent-first", nil)
		}
		var wg sync.WaitGroup
		var errs [2]error
		for i := range requests {
			wg.Add(1)
			go func() { defer wg.Done(); _, errs[i] = l.clients[i].CompleteGoogleLogin(t.Context(), requests[i]) }()
		}
		wg.Wait()
		for _, err := range errs {
			must(t, err)
		}
		var count int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(DISTINCT account_id) FROM identity.external_identities WHERE subject='concurrent-first'`).Scan(&count))
		if count != 1 {
			t.Fatal("duplicate account")
		}
	})
	t.Run("simultaneous starts with old cookie leave one pending flow", func(t *testing.T) {
		old, err := l.clients[0].StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
		must(t, err)
		var wg sync.WaitGroup
		var errs [2]error
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = l.clients[i].StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{PreviousFlowCookie: old.FlowCookie})
			}()
		}
		wg.Wait()
		for _, err := range errs {
			must(t, err)
		}
		var count int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.login_flows WHERE family_id=(SELECT family_id FROM identity.login_flows WHERE cookie_hash=$1) AND status='pending'`, digest(old.FlowCookie)).Scan(&count))
		if count != 1 {
			t.Fatal("multiple replacement flows remained active")
		}
	})
	badKey, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	for name, change := range map[string]func(*oidcCode){
		"wrong nonce":                    func(c *oidcCode) { c.claims = jwt.MapClaims{"nonce": "wrong"} },
		"wrong audience":                 func(c *oidcCode) { c.claims = jwt.MapClaims{"aud": "attacker"} },
		"wrong issuer":                   func(c *oidcCode) { c.claims = jwt.MapClaims{"iss": "https://attacker.test"} },
		"expired token":                  func(c *oidcCode) { c.claims = jwt.MapClaims{"exp": time.Now().Add(-time.Minute).Unix()} },
		"wrong azp":                      func(c *oidcCode) { c.claims = jwt.MapClaims{"azp": "attacker"} },
		"multiple audiences require azp": func(c *oidcCode) { c.claims = jwt.MapClaims{"aud": []string{"test-client", "other"}} },
		"empty subject":                  func(c *oidcCode) { c.subject = "" },
		"invalid signature":              func(c *oidcCode) { c.key = badKey },
		"wrong PKCE verifier":            func(c *oidcCode) { c.query.Set("code_challenge", "wrong") },
	} {
		t.Run(name, func(t *testing.T) {
			_, req := newFlow(t, "rejected-"+name, change)
			_, err := l.clients[1].CompleteGoogleLogin(t.Context(), req)
			grpcCode(t, err, codes.Unauthenticated)
		})
	}
	t.Run("state cookie expiry and cancellation", func(t *testing.T) {
		flow, req := newFlow(t, "state", nil)
		before := l.provider.exchanges.Load()
		state := req.State
		req.State = "wrong"
		_, err := l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.InvalidArgument)
		req.State = state
		cookie := req.FlowCookie
		req.FlowCookie = "wrong"
		_, err = l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.InvalidArgument)
		req.FlowCookie = cookie
		_, err = l.db.Exec(t.Context(), `UPDATE identity.login_flows SET expires_at=clock_timestamp()-interval '1 second' WHERE cookie_hash=$1`, digest(flow.FlowCookie))
		must(t, err)
		_, err = l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.InvalidArgument)
		_, req = newFlow(t, "denied", nil)
		req.Outcome = &pb.CompleteGoogleLoginRequest_ProviderError{ProviderError: "access_denied"}
		response, err := l.clients[1].CompleteGoogleLogin(t.Context(), req)
		must(t, err)
		if response.RedirectPath != "/?login=cancelled" || response.SessionCookie != "" {
			t.Fatal("denial changed session")
		}
		if before != l.provider.exchanges.Load() {
			t.Fatal("invalid flow reached token exchange")
		}
		must(t, l.servers[0].Cleanup(t.Context()))
		var cleared bool
		must(t, l.db.QueryRow(t.Context(), `SELECT status='expired' AND verifier_cipher IS NULL FROM identity.login_flows WHERE cookie_hash=$1`, digest(flow.FlowCookie)).Scan(&cleared))
		if !cleared {
			t.Fatal("cleanup retained expired verifier")
		}
	})
	t.Run("provider failures and lost responses cannot replay codes", func(t *testing.T) {
		for _, code := range []int{429, 500} {
			_, req := newFlow(t, "unavailable", func(c *oidcCode) { c.status = code })
			_, err := l.clients[0].CompleteGoogleLogin(t.Context(), req)
			grpcCode(t, err, codes.Unavailable)
			before := l.provider.exchanges.Load()
			_, err = l.clients[1].CompleteGoogleLogin(t.Context(), req)
			grpcCode(t, err, codes.InvalidArgument)
			if before != l.provider.exchanges.Load() {
				t.Fatal("failed code was replayed")
			}
		}
		entered, release := make(chan struct{}), make(chan struct{})
		defer close(release)
		flow, req := newFlow(t, "lost-response", func(c *oidcCode) { c.entered = entered; c.release = release })
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { _, err := l.clients[0].CompleteGoogleLogin(ctx, req); done <- err }()
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("exchange did not start")
		}
		cancel()
		grpcCode(t, <-done, codes.Canceled)
		deadline := time.Now().Add(3 * time.Second)
		for {
			var state string
			must(t, l.db.QueryRow(t.Context(), `SELECT status FROM identity.login_flows WHERE cookie_hash=$1`, digest(flow.FlowCookie)).Scan(&state))
			if state == "failed" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("cancelled exchange not cleaned")
			}
			time.Sleep(10 * time.Millisecond)
		}
		_, err := l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.InvalidArgument)
	})
}

func TestServiceIdentityAssertionsAndOutages(t *testing.T) {
	l := newIdentityLab(t)
	t.Run("streaming health enforces the same service boundary", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()
		for _, caller := range []string{"content", "gateway"} {
			watch, err := healthpb.NewHealthClient(l.connection(t, 0, caller, l.pki)).Watch(ctx, &healthpb.HealthCheckRequest{})
			must(t, err)
			_, err = watch.Recv()
			if caller == "content" {
				grpcCode(t, err, codes.PermissionDenied)
			} else {
				must(t, err)
			}
		}
	})
	session, me := l.login(t, "assertion-user", nil)
	method := "/humanworth.content.v1.ContentService/GetTask"
	actor, err := l.clients[0].ResolvePrincipal(t.Context(), &pb.ResolvePrincipalRequest{Audience: "content", FullMethod: method, Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: session.Value}})
	must(t, err)
	content := l.client(t, 1, "content", l.pki)
	voting := l.client(t, 1, "voting", l.pki)
	_, err = content.StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
	grpcCode(t, err, codes.PermissionDenied)
	_, err = voting.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: method})
	grpcCode(t, err, codes.PermissionDenied)
	_, err = content.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: "/humanworth.voting.v1.VotingService/CastVote"})
	grpcCode(t, err, codes.PermissionDenied)
	_, err = l.clients[0].VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion, FullMethod: method})
	grpcCode(t, err, codes.PermissionDenied)
	_, err = content.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: actor.ActorAssertion + "x", FullMethod: method})
	grpcCode(t, err, codes.Unauthenticated)
	untrusted := l.client(t, 0, "gateway", newTestPKI(t))
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = untrusted.StartGoogleLogin(ctx, &pb.StartGoogleLoginRequest{})
	if err == nil {
		t.Fatal("untrusted service certificate accepted")
	}
	unknown := l.client(t, 0, "attacker", l.pki)
	_, err = unknown.StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
	grpcCode(t, err, codes.Unauthenticated)
	var claims actorClaims
	_, _, err = jwt.NewParser().ParseUnverified(actor.ActorAssertion, &claims)
	must(t, err)
	claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Second))
	key, err := l.servers[0].Config.Signing.key("key1")
	must(t, err)
	expired := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	expired.Header["kid"] = "key1"
	raw, err := expired.SignedString(key)
	must(t, err)
	_, err = content.VerifyActor(t.Context(), &pb.VerifyActorRequest{ActorAssertion: raw, FullMethod: method})
	grpcCode(t, err, codes.Unauthenticated)

	t.Run("rolling key rotation accepts old key only during overlap", func(t *testing.T) {
		cfg := l.servers[0].Config
		oldKey := cfg.Signing.Keys["key1"]
		cfg.Signing = KeyRing{Active: "key2", Keys: map[string]string{"key1": oldKey, "key2": base64.StdEncoding.EncodeToString([]byte(randomToken()[:32]))}}
		rotated, err := NewServer(l.runtimeDB, cfg)
		must(t, err)
		row, err := rotated.verify(t.Context(), actor.ActorAssertion, "content", method)
		must(t, err)
		if row.Principal.AccountId != me.Account.Id {
			t.Fatal("rotation changed principal")
		}
		delete(cfg.Signing.Keys, "key1")
		_, err = rotated.verify(t.Context(), actor.ActorAssertion, "content", method)
		grpcCode(t, err, codes.Unauthenticated)
	})
	t.Run("JWKS failure is unavailable", func(t *testing.T) {
		p := l.provider.server.URL
		fresh := newOAuth(t.Context(), "test-client", "test-secret", l.origin+"/api/auth/google/callback", "test-v1", p, p+"/auth", p+"/token", p+"/jwks")
		cfg := l.servers[0].Config
		cfg.OAuth = fresh
		s, err := NewServer(l.runtimeDB, cfg)
		must(t, err)
		f, err := s.StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
		must(t, err)
		u, _ := url.Parse(f.AuthorizationUrl)
		code := l.provider.issue(t, f.AuthorizationUrl, "jwks", nil)
		l.provider.jwksUnavailable.Store(true)
		defer l.provider.jwksUnavailable.Store(false)
		_, err = s.CompleteGoogleLogin(t.Context(), &pb.CompleteGoogleLoginRequest{FlowCookie: f.FlowCookie, State: u.Query().Get("state"), Outcome: &pb.CompleteGoogleLoginRequest_Code{Code: code}})
		grpcCode(t, err, codes.Unavailable)
	})
	t.Run("database outage fails closed", func(t *testing.T) {
		var dbName string
		must(t, l.db.QueryRow(t.Context(), `SELECT current_database()`).Scan(&dbName))
		_, err := l.admin.Exec(t.Context(), "ALTER DATABASE "+pgx.Identifier{dbName}.Sanitize()+" ALLOW_CONNECTIONS false")
		must(t, err)
		defer l.admin.Exec(context.Background(), "ALTER DATABASE "+pgx.Identifier{dbName}.Sanitize()+" ALLOW_CONNECTIONS true")
		_, err = l.db.Exec(t.Context(), `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND usename<>current_user`)
		must(t, err)
		httpStatus(t, l.request(t, 1, "GET", "/api/me", []*http.Cookie{session}, "", nil), 503)
		if Ready(t.Context(), l.runtimeDB) == nil {
			t.Fatal("readiness remained healthy without database")
		}
	})
}
