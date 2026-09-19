//go:build integration

package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// A real HTTP OIDC provider exercises code exchange, PKCE, JWT and JWKS verification.
// It exists only in integration tests; production endpoints cannot be overridden.
type oidcCode struct {
	query   url.Values
	subject string
	claims  jwt.MapClaims
	key     *rsa.PrivateKey
	entered chan struct{}
	release chan struct{}
	status  int
}
type oidcTestProvider struct {
	server          *httptest.Server
	key             *rsa.PrivateKey
	mu              sync.Mutex
	codes           map[string]oidcCode
	exchanges       atomic.Int64
	jwksUnavailable atomic.Bool
}

func newTestProvider(t *testing.T) *oidcTestProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	must(t, err)
	p := &oidcTestProvider{key: key, codes: map[string]oidcCode{}}
	p.server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.server.Close)
	return p
}
func (p *oidcTestProvider) issue(t *testing.T, authURL, subject string, change func(*oidcCode)) string {
	t.Helper()
	u, err := url.Parse(authURL)
	must(t, err)
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || len(q.Get("code_challenge")) != 43 || q.Get("nonce") == "" || q.Get("state") == "" || q.Get("scope") != "openid email profile" || q.Get("response_type") != "code" {
		t.Fatal("authorization request does not satisfy OIDC/PKCE contract")
	}
	c := oidcCode{query: q, subject: subject, key: p.key}
	if change != nil {
		change(&c)
	}
	code := randomToken()
	p.mu.Lock()
	p.codes[code] = c
	p.mu.Unlock()
	return code
}
func (p *oidcTestProvider) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/jwks" {
		if p.jwksUnavailable.Load() {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "test-key", "n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": "AQAB"}}})
		return
	}
	if r.URL.Path != "/token" || r.Method != "POST" {
		w.WriteHeader(404)
		return
	}
	p.exchanges.Add(1)
	if r.ParseForm() != nil {
		w.WriteHeader(400)
		return
	}
	p.mu.Lock()
	c, ok := p.codes[r.Form.Get("code")]
	delete(p.codes, r.Form.Get("code"))
	p.mu.Unlock()
	challenge := base64.RawURLEncoding.EncodeToString(digest(r.Form.Get("code_verifier")))
	if !ok || r.Form.Get("client_id") != "test-client" || r.Form.Get("client_secret") != "test-secret" || r.Form.Get("redirect_uri") != c.query.Get("redirect_uri") || r.Form.Get("grant_type") != "authorization_code" || challenge != c.query.Get("code_challenge") {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":"invalid_grant"}`)
		return
	}
	if c.entered != nil {
		close(c.entered)
		select {
		case <-c.release:
		case <-r.Context().Done():
			return
		}
	}
	if c.status != 0 {
		w.WriteHeader(c.status)
		return
	}
	claims := jwt.MapClaims{"iss": p.server.URL, "sub": c.subject, "aud": "test-client", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": c.query.Get("nonce"), "name": "测试用户 <script>", "email": "same@example.test", "email_verified": true}
	for k, v := range c.claims {
		claims[k] = v
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = "test-key"
	raw, err := token.SignedString(c.key)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"access_token": "test-unused-access", "token_type": "Bearer", "expires_in": 3600, "id_token": raw})
}

type testPKI struct {
	dir string
	ca  *x509.Certificate
	key *ecdsa.PrivateKey
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	p := &testPKI{dir: t.TempDir()}
	var err error
	p.key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	p.ca = &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true, BasicConstraintsValid: true, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, p.ca, p.ca, &p.key.PublicKey, p.key)
	must(t, err)
	must(t, os.WriteFile(filepath.Join(p.dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	return p
}
func (p *testPKI) cert(t *testing.T, name string) (string, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	uri, err := url.Parse(platform.ServiceURIPrefix + name)
	must(t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	must(t, err)
	cert := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{name}, URIs: []*url.URL{uri}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, p.ca, &key.PublicKey, p.key)
	must(t, err)
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	must(t, err)
	certPath, keyPath := filepath.Join(p.dir, name+".pem"), filepath.Join(p.dir, name+"-key.pem")
	must(t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	must(t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600))
	return certPath, keyPath, filepath.Join(p.dir, "ca.pem")
}

type identityLab struct {
	db, runtimeDB *pgxpool.Pool
	admin         *pgxpool.Pool
	provider      *oidcTestProvider
	pki           *testPKI
	servers       [2]*Server
	grpc          [2]*grpc.Server
	addresses     [2]string
	clients       [2]pb.IdentityServiceClient
	web           [2]*httptest.Server
	origin        string
}

func newIdentityLab(t *testing.T) *identityLab {
	t.Helper()
	ctx := t.Context()
	dsn := os.Getenv("IDENTITY_TEST_DATABASE_URL")
	if path := os.Getenv("IDENTITY_TEST_DATABASE_URL_FILE"); path != "" {
		b, err := os.ReadFile(path)
		must(t, err)
		dsn = strings.TrimSpace(string(b))
	}
	if dsn == "" {
		t.Fatal("integration requires an isolated PostgreSQL server via IDENTITY_TEST_DATABASE_URL[_FILE]")
	}
	admin, err := pgxpool.New(ctx, dsn)
	must(t, err)
	name := "hw_test_" + strings.ToLower(randomToken()[:12])
	name = strings.ReplaceAll(name, "-", "x")
	_, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	must(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	must(t, err)
	cfg.ConnConfig.Database = name
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	roleName := name + "_runtime"
	ownerName := name + "_owner"
	password := randomToken()
	t.Cleanup(func() {
		db.Close()
		admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{roleName}.Sanitize())
		admin.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{ownerName}.Sanitize())
		admin.Close()
	})
	_, err = db.Exec(ctx, "CREATE ROLE "+pgx.Identifier{ownerName}.Sanitize()+" LOGIN PASSWORD '"+password+"'; REVOKE CREATE ON DATABASE "+pgx.Identifier{name}.Sanitize()+" FROM PUBLIC; CREATE SCHEMA identity AUTHORIZATION "+pgx.Identifier{ownerName}.Sanitize())
	must(t, err)
	ownerCfg := cfg.Copy()
	ownerCfg.ConnConfig.User = ownerName
	ownerCfg.ConnConfig.Password = password
	ownerDB, err := pgxpool.NewWithConfig(ctx, ownerCfg)
	must(t, err)
	t.Cleanup(ownerDB.Close)
	// The migration role owns only its schema, not the database or other schemas.
	must(t, Migrate(ctx, ownerDB))
	must(t, Migrate(ctx, ownerDB))
	if _, err = ownerDB.Exec(ctx, "CREATE SCHEMA must_be_denied"); err == nil {
		t.Fatal("migration role unexpectedly owns database-wide CREATE permission")
	}
	_, err = db.Exec(ctx, "CREATE ROLE "+pgx.Identifier{roleName}.Sanitize()+" LOGIN PASSWORD '"+password+"'")
	must(t, err)
	roleID := pgx.Identifier{roleName}.Sanitize()
	_, err = db.Exec(ctx, `REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA unrelated; CREATE TABLE unrelated.private_data(secret text);
GRANT USAGE ON SCHEMA identity TO `+roleID+`;
GRANT SELECT ON ALL TABLES IN SCHEMA identity TO `+roleID+`;
GRANT INSERT(id,display_name),UPDATE(display_name,updated_at) ON identity.accounts TO `+roleID+`;
GRANT INSERT,UPDATE,DELETE ON identity.external_identities,identity.credentials,identity.login_families,identity.login_flows TO `+roleID+`;
GRANT INSERT ON identity.audit_events TO `+roleID+`;`)
	must(t, err)
	runtimeCfg := cfg.Copy()
	runtimeCfg.ConnConfig.User = roleName
	runtimeCfg.ConnConfig.Password = password
	runtimeCfg.MaxConns = 8
	runtimeDB, err := pgxpool.NewWithConfig(ctx, runtimeCfg)
	must(t, err)
	t.Cleanup(runtimeDB.Close)
	lab := &identityLab{db: db, runtimeDB: runtimeDB, admin: admin, provider: newTestProvider(t), pki: newTestPKI(t)}
	for i := range lab.web {
		lab.web[i] = httptest.NewUnstartedServer(nil)
		t.Cleanup(lab.web[i].Close)
	}
	lab.origin = "https://" + lab.web[0].Listener.Addr().String()
	ring := KeyRing{Active: "key1", Keys: map[string]string{"key1": base64.StdEncoding.EncodeToString([]byte(randomToken()[:32]))}}
	for i := range lab.servers {
		p := lab.provider.server.URL
		oauth := newOAuth(ctx, "test-client", "test-secret", lab.origin+"/api/auth/google/callback", "test-v1", p, p+"/auth", p+"/token", p+"/jwks")
		lab.servers[i], err = NewServer(runtimeDB, Config{Origin: lab.origin, Encryption: ring, Signing: ring, OAuth: oauth})
		must(t, err)
		cert, key, ca := lab.pki.cert(t, "identity")
		tlsConfig, err := platform.TLS(cert, key, ca, "")
		must(t, err)
		lab.grpc[i] = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.UnaryInterceptor(Authorization), grpc.StreamInterceptor(AuthorizationStream))
		pb.RegisterIdentityServiceServer(lab.grpc[i], lab.servers[i])
		healthpb.RegisterHealthServer(lab.grpc[i], health.NewServer())
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		lab.addresses[i] = listener.Addr().String()
		go lab.grpc[i].Serve(listener)
		t.Cleanup(lab.grpc[i].Stop)
		lab.clients[i] = lab.client(t, i, "gateway", lab.pki)
		handler, err := gateway.New(lab.clients[i], gateway.Options{Origin: lab.origin, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		must(t, err)
		lab.web[i].Config.Handler = handler
		lab.web[i].StartTLS()
		lab.web[i].Client().CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return lab
}
func (l *identityLab) client(t *testing.T, replica int, service string, pki *testPKI) pb.IdentityServiceClient {
	return pb.NewIdentityServiceClient(l.connection(t, replica, service, pki))
}
func (l *identityLab) connection(t *testing.T, replica int, service string, pki *testPKI) *grpc.ClientConn {
	t.Helper()
	cert, key, ca := pki.cert(t, service)
	cfg, err := platform.TLS(cert, key, ca, "identity")
	must(t, err)
	conn, err := grpc.NewClient(l.addresses[replica], grpc.WithTransportCredentials(credentials.NewTLS(cfg)), grpc.WithDisableRetry())
	must(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func (l *identityLab) request(t *testing.T, replica int, method, path string, cookies []*http.Cookie, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, l.web[replica].URL+path, strings.NewReader(body))
	must(t, err)
	u, _ := url.Parse(l.origin)
	req.Host = u.Host
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := l.web[replica].Client().Do(req)
	must(t, err)
	t.Cleanup(func() { response.Body.Close() })
	return response
}
func httpStatus(t *testing.T, r *http.Response, expected int) {
	t.Helper()
	if r.StatusCode != expected {
		b, _ := io.ReadAll(r.Body)
		t.Fatalf("HTTP status=%d want=%d response=%s", r.StatusCode, expected, b)
	}
}
func readJSON(t *testing.T, r *http.Response, v any) {
	t.Helper()
	must(t, json.NewDecoder(r.Body).Decode(v))
	r.Body.Close()
}
func findCookie(t *testing.T, r *http.Response, name string) *http.Cookie {
	t.Helper()
	for _, c := range r.Cookies() {
		if c.Name == name {
			if !c.Secure || !c.HttpOnly || c.Path != "/" || c.Domain != "" || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("unsafe cookie attributes")
			}
			return c
		}
	}
	t.Fatal("cookie not set: " + name)
	return nil
}
func (l *identityLab) login(t *testing.T, subject string, old *http.Cookie) (*http.Cookie, *pb.GetCurrentSessionResponse) {
	t.Helper()
	start := l.request(t, 0, "GET", "/api/auth/google", nil, "", nil)
	httpStatus(t, start, 302)
	flow := findCookie(t, start, gateway.FlowCookie)
	u, _ := url.Parse(start.Header.Get("Location"))
	code := l.provider.issue(t, u.String(), subject, nil)
	cookies := []*http.Cookie{flow}
	if old != nil {
		cookies = append(cookies, old)
	}
	response := l.request(t, 1, "GET", "/api/auth/google/callback?"+url.Values{"state": {u.Query().Get("state")}, "code": {code}}.Encode(), cookies, "", nil)
	httpStatus(t, response, 303)
	session := findCookie(t, response, gateway.SessionCookie)
	me := l.request(t, 0, "GET", "/api/me", []*http.Cookie{session}, "", nil)
	httpStatus(t, me, 200)
	var value struct {
		Account struct {
			ID   string `json:"id"`
			Name string `json:"displayName"`
		} `json:"account"`
		Role string `json:"role"`
		CSRF string `json:"csrfToken"`
	}
	readJSON(t, me, &value)
	return session, &pb.GetCurrentSessionResponse{Account: &pb.Account{Id: value.Account.ID, DisplayName: value.Account.Name}, Role: role(value.Role), CsrfToken: value.CSRF}
}
