//go:build lab

package identity

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Synthetic credentials exist only in validate_modules.py's disposable namespace.
func TestKindContentDeployment(t *testing.T) {
	namespace := os.Getenv("HUMAN_WORTH_VERIFY_NAMESPACE")
	if namespace == "" {
		t.Skip("run ops/lab/validate_modules.py")
	}
	if !strings.HasPrefix(namespace, "human-worth-verify-") || namespace == "human-worth" {
		t.Fatal("refusing a non-disposable namespace")
	}
	state, config := os.Getenv("HUMAN_WORTH_VERIFY_STATE"), os.Getenv("HUMAN_WORTH_VERIFY_KUBECONFIG")
	kube := func(input string, args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "kubectl", append([]string{"--kubeconfig", config, "--context", "kind-lab", "-n", namespace}, args...)...)
		cmd.Stdin = strings.NewReader(input)
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("temporary namespace command failed: %v", args)
		}
		return output
	}
	primary := string(kube("", "get", "cluster", "database", "-o", "jsonpath={.status.currentPrimary}"))
	sql := func(query string) {
		kube(query, "exec", "-i", primary, "--", "psql", "-X", "-U", "postgres", "-d", "human_worth", "-v", "ON_ERROR_STOP=1")
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
	hash := sha256.Sum256([]byte(session))
	sql(fmt.Sprintf("INSERT INTO identity.accounts(id,display_name) VALUES('%s','deployment test'); INSERT INTO identity.credentials(id,account_id,kind,token_hash,auth_version,csrf_cipher,expires_at) VALUES('%s','%s','web',decode('%s','hex'),1,'%s',clock_timestamp()+interval '15 minutes');", account, credential, account, hex.EncodeToString(hash[:]), cipher))
	ca, err := os.ReadFile(filepath.Join(state, "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid test CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", "127.0.0.1:"+os.Getenv("HUMAN_WORTH_VERIFY_PORT"))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	request := func(method, path, body string, authenticated bool, expected int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, "https://worth.oopsbox.cn"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "https://worth.oopsbox.cn")
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Idempotency-Key", "deployment-check-"+account)
		if authenticated {
			req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: session})
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("deployment HTTPS request failed")
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != expected {
			t.Fatalf("%s %s: got %d, want %d", method, path, response.StatusCode, expected)
		}
		return data
	}
	request("GET", "/api/me", "", true, 200)
	body := `{"title":"deployment check","summary":"temporary","description":"isolated","entries":[]}`
	request("POST", "/api/tasks", body, false, 401)
	data := request("POST", "/api/tasks", body, true, 201)
	var draft struct {
		ID       string `json:"id"`
		AuthorID string `json:"authorId"`
		Revision int    `json:"revision"`
	}
	if json.Unmarshal(data, &draft) != nil || draft.ID == "" || draft.AuthorID != account || draft.Revision != 1 {
		t.Fatal("invalid deployed draft result")
	}
	path := "/api/me/tasks/" + draft.ID
	request("GET", path, "", false, 401)
	request("GET", path, "", true, 200)
	update := `{"expectedRevision":1,"content":{"title":"updated deployment check","summary":"","description":"","entries":[]}}`
	request("PUT", path, update, true, 200)
	request("PUT", path, update, true, 409)
	pods := strings.Fields(string(kube("", "get", "pods", "-l", "app=content", "-o", "jsonpath={.items[*].metadata.name}")))
	if len(pods) != 2 {
		t.Fatalf("expected two Content replicas, got %d", len(pods))
	}
	kube("", "delete", "pod", pods[0], "--wait=true")
	kube("", "rollout", "status", "deployment/content", "--timeout=120s")
	data = request("GET", path, "", true, 200)
	if json.Unmarshal(data, &draft) != nil || draft.Revision != 2 || !strings.Contains(string(data), "updated deployment check") {
		t.Fatal("draft did not survive Content restart")
	}
	request("POST", "/api/auth/logout", "", true, 204)
	request("GET", path, "", true, 401)
	t.Log("HTTPS -> gateway -> Identity -> Content -> PostgreSQL: create/read/replace, conflict, restart persistence and revocation passed")
}
