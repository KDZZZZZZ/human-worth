//go:build lab

package identity

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Explicit opt-in: creates synthetic lab data and replaces an Identity Pod and
// the PostgreSQL primary. It never contacts the public application.
func TestKindIdentity(t *testing.T) {
	observedSince := time.Now().UTC().Format(time.RFC3339Nano)
	if os.Getenv("HUMAN_WORTH_LAB_CHECK") != "1" {
		t.Skip("set HUMAN_WORTH_LAB_CHECK=1 for isolated kind-lab")
	}
	state := os.Getenv("HUMAN_WORTH_LAB_DIR")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".config/human-worth/lab")
	}
	config := filepath.Join(filepath.Dir(state), "lab.kubeconfig")
	kube := func(input string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", config, "--context", "kind-lab", "-n", "human-worth"}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("lab kubectl failed (%v), inspect cluster events", args)
		}
		return output
	}
	if names := string(kube("", "get", "nodes", "-o", "name")); !strings.Contains(names, "node/lab-worker3") || !strings.Contains(names, "node/lab-control-plane") {
		t.Fatal("unexpected cluster")
	}
	primary := func() string {
		return string(kube("", "get", "cluster", "database", "-o", "jsonpath={.status.currentPrimary}"))
	}
	sql := func(statement string) string {
		return strings.TrimSpace(string(kube(statement, "exec", "-i", primary(), "--", "psql", "-X", "-U", "postgres", "-d", "human_worth", "-At", "-v", "ON_ERROR_STOP=1")))
	}
	ring, err := LoadKeyRing(filepath.Join(state, "encryption-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	account, credential, session, csrf := newID("acct"), newID("cred"), randomToken(), randomToken()
	cipher, err := ring.seal(csrf, credential+":csrf")
	if err != nil {
		t.Fatal(err)
	}
	sql(fmt.Sprintf("INSERT INTO identity.accounts(id,display_name) VALUES('%s','lab synthetic'); INSERT INTO identity.credentials(id,account_id,kind,token_hash,auth_version,csrf_cipher,expires_at) VALUES('%s','%s','web',decode('%s','hex'),1,'%s',clock_timestamp()+interval '30 minutes');", account, credential, account, hex.EncodeToString(digest(session)), cipher))
	// Audit rows remain as evidence, without the synthetic raw credentials.
	defer func() {
		sql(fmt.Sprintf("DELETE FROM identity.credentials WHERE account_id='%s'; DELETE FROM identity.accounts WHERE id='%s';", account, account))
	}()
	ca, err := os.ReadFile(filepath.Join(state, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("lab CA invalid")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:18443")
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request := func(method, path, body string, authenticated, write bool) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, "https://worth.oopsbox.cn"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal("invalid lab request")
		}
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: session})
		}
		if write {
			req.Header.Set("Origin", "https://worth.oopsbox.cn")
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("lab HTTPS request failed")
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal("lab response incomplete")
		}
		return response.StatusCode, response.Header, data
	}
	expect := func(want int, method, path, body string, authenticated, write bool) []byte {
		t.Helper()
		code, _, data := request(method, path, body, authenticated, write)
		if code != want {
			t.Fatalf("%s %s: HTTP %d, want %d", method, strings.Split(path, "?")[0], code, want)
		}
		return data
	}
	expect(200, "GET", "/api/health", "", false, false)
	expect(200, "GET", "/brand/logo.png", "", false, false)
	expect(401, "GET", "/api/me", "", false, false)
	code, headers, _ := request("GET", "/api/auth/google", "", false, false)
	location, err := url.Parse(headers.Get("Location"))
	if err != nil || code != 302 || location.Host != "accounts.google.com" {
		t.Fatal("Google redirect missing")
	}
	q := location.Query()
	if q.Get("redirect_uri") != "https://worth.oopsbox.cn/api/auth/google/callback" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" || q.Get("nonce") == "" {
		t.Fatal("Google redirect contract invalid")
	}
	// Invalid real code checks configured Google HTTPS egress, not human consent.
	callback, _ := http.NewRequestWithContext(t.Context(), "GET", "https://worth.oopsbox.cn/api/auth/google/callback?state="+url.QueryEscape(q.Get("state"))+"&code=lab-invalid-code", nil)
	for _, cookie := range (&http.Response{Header: headers}).Cookies() {
		callback.AddCookie(cookie)
	}
	response, err := client.Do(callback)
	if err != nil {
		t.Fatal("real Google error-path transport failed")
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatalf("Google invalid-code path returned %d, want 401", response.StatusCode)
	}
	t.Log("Google authorization URL and real token-endpoint rejection verified; human login remains separate")
	for i := 0; i < 24; i++ {
		data := expect(200, "GET", "/api/me", "", true, false)
		var me struct {
			Account   struct{ ID string }
			CSRFToken string
		}
		if json.Unmarshal(data, &me) != nil || me.Account.ID != account || me.CSRFToken != csrf {
			t.Fatal("unstable session")
		}
	}
	expect(403, "POST", "/api/auth/logout", "", true, false)
	body := `{"name":"lab synthetic","createRequestId":"` + randomToken() + `"}`
	data := expect(201, "POST", "/api/me/mcp-tokens", body, true, true)
	var created struct {
		Credential struct{ ID string }
		Token      string
	}
	if json.Unmarshal(data, &created) != nil || created.Token == "" {
		t.Fatal("missing MCP credential")
	}
	expect(409, "POST", "/api/me/mcp-tokens", body, true, true)
	expect(204, "DELETE", "/api/me/mcp-tokens/"+created.Credential.ID, "", true, true)
	pods := strings.Fields(string(kube("", "get", "pods", "-l", "app=identity", "-o", "jsonpath={.items[*].metadata.name}")))
	if len(pods) != 2 {
		t.Fatalf("want 2 Identity pods, got %d", len(pods))
	}
	for _, pod := range pods {
		logs := string(kube("", "logs", pod, "--since-time="+observedSince))
		if !strings.Contains(logs, "GetCurrentSession") && !strings.Contains(logs, "ResolvePrincipal") {
			t.Fatal("replica received no session RPC: " + pod)
		}
		t.Logf("%s: ResolvePrincipal=%d GetCurrentSession=%d", pod, strings.Count(logs, "ResolvePrincipal"), strings.Count(logs, "GetCurrentSession"))
	}
	started := time.Now()
	kube("", "delete", "pod", pods[0], "--wait=true", "--timeout=60s")
	kube("", "rollout", "status", "deployment/identity", "--timeout=150s")
	expect(200, "GET", "/api/me", "", true, false)
	t.Logf("Identity Pod replaced, session preserved (%s)", time.Since(started).Round(time.Second))
	oldPrimary := primary()
	started = time.Now()
	kube("", "delete", "pod", oldPrimary, "--wait=false")
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		status := string(kube("", "get", "cluster", "database", "-o", "jsonpath={.status.currentPrimary}:{.status.readyInstances}"))
		if strings.HasSuffix(status, ":3") && !strings.HasPrefix(status, oldPrimary+":") {
			break
		}
		time.Sleep(time.Second)
	}
	if primary() == oldPrimary {
		t.Fatal("primary did not fail over")
	}
	kube("", "wait", "--for=condition=Ready", "cluster/database", "--timeout=150s")
	expect(200, "GET", "/api/me", "", true, false)
	data = expect(200, "GET", "/api/me/mcp-tokens", "", true, false)
	if !strings.Contains(string(data), `"state":"revoked"`) || !strings.Contains(string(data), created.Credential.ID) {
		t.Fatal("acknowledged revocation missing after failover")
	}
	if sql("SELECT count(*) FROM identity.accounts WHERE id='"+account+"'") != "1" {
		t.Fatal("committed account lost")
	}
	t.Logf("PostgreSQL primary %s -> %s; account/revocation preserved (%s)", oldPrimary, primary(), time.Since(started).Round(time.Second))
	expect(204, "POST", "/api/auth/logout", "", true, true)
	for i := 0; i < 12; i++ {
		expect(401, "GET", "/api/me", "", true, false)
	}
	script, err := filepath.Abs("../../../ops/lab/account.py")
	if err != nil {
		t.Fatal(err)
	}
	operator := exec.CommandContext(t.Context(), "python3", script, "--account", account, "--role", "admin", "--state", "active", "--expected-version", "1", "--operator", "lab-verifier")
	if _, err = operator.CombinedOutput(); err != nil {
		t.Fatal("restricted account maintenance Job failed")
	}
	if sql("SELECT role||':'||auth_version FROM identity.accounts WHERE id='"+account+"'") != "admin:2" {
		t.Fatal("maintenance Job did not update the synthetic account version")
	}
	if sql("SELECT count(*) FROM identity.audit_events WHERE actor_id='lab-verifier' AND action='account_change' AND target_id='"+account+"'") != "1" {
		t.Fatal("maintenance audit missing")
	}
	t.Log("restricted operator Job and atomic audit verified on the synthetic account")
}
