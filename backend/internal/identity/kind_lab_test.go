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
	"regexp"
	"strings"
	"testing"
	"time"
)

// Agent Self-Claimed: shared fixture for the disposable kind namespace only.
// It creates synthetic sessions, never reads a real user's credentials, and
// leaves resource deletion to validate_modules.py's namespace cleanup.
type kindLab struct {
	t                        *testing.T
	namespace, state, config string
	primary                  string
	client                   *http.Client
}

type kindUser struct{ account, session, csrf string }

func newKindLab(t *testing.T) *kindLab {
	t.Helper()
	l := &kindLab{t: t, namespace: os.Getenv("HUMAN_WORTH_VERIFY_NAMESPACE"), state: os.Getenv("HUMAN_WORTH_VERIFY_STATE"), config: os.Getenv("HUMAN_WORTH_VERIFY_KUBECONFIG")}
	if l.namespace == "" {
		t.Skip("INCONCLUSIVE: run ops/lab/validate_modules.py")
	}
	if !regexp.MustCompile(`^human-worth-verify-[a-f0-9]{8}$`).MatchString(l.namespace) || l.state == "" || l.config == "" {
		t.Fatal("refusing a non-disposable or incomplete kind configuration")
	}
	l.primary = string(l.kube("", "get", "cluster", "database", "-o", "jsonpath={.status.currentPrimary}"))
	l.client = l.httpClient(os.Getenv("HUMAN_WORTH_VERIFY_PORT"))
	return l
}

func (l *kindLab) command(ctx context.Context, input string, args ...string) *exec.Cmd {
	kubeContext := os.Getenv("HUMAN_WORTH_VERIFY_CONTEXT")
	if kubeContext == "" {
		kubeContext = "kind-lab"
	}
	if kubeContext != "kind-lab" && kubeContext != "kind-human-worth-backend-check" {
		l.t.Fatal("isolated test cluster required")
	}
	cmd := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", l.config, "--context", kubeContext, "-n", l.namespace}, args...)...)
	cmd.Stdin = strings.NewReader(input)
	return cmd
}

func (l *kindLab) kube(input string, args ...string) []byte {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(l.t.Context(), 150*time.Second)
	defer cancel()
	output, err := l.command(ctx, input, args...).Output()
	if err != nil {
		// stdin/output may contain Secret material. Never print either.
		l.t.Fatalf("temporary namespace command failed: %v (%v)", args, err)
	}
	return output
}

func (l *kindLab) sql(query string) string {
	l.t.Helper()
	return strings.TrimSpace(string(l.kube(query, "exec", "-i", l.primary, "--", "psql", "-X", "-U", "postgres", "-d", "human_worth", "-At", "-v", "ON_ERROR_STOP=1")))
}

func (l *kindLab) user() kindUser {
	l.t.Helper()
	ring, err := LoadKeyRing(filepath.Join(l.state, "encryption-keys.json"))
	if err != nil {
		l.t.Fatal(err)
	}
	u := kindUser{newID("acct"), randomToken(), randomToken()}
	credential := newID("cred")
	cipher, err := ring.Seal(u.csrf, credential+":csrf")
	if err != nil {
		l.t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(u.session))
	l.sql(fmt.Sprintf("INSERT INTO identity.accounts(id,display_name) VALUES('%s','deployment test'); INSERT INTO identity.credentials(id,account_id,kind,token_hash,auth_version,csrf_cipher,expires_at) VALUES('%s','%s','web',decode('%s','hex'),1,'%s',clock_timestamp()+interval '15 minutes');", u.account, credential, u.account, hex.EncodeToString(hash[:]), cipher))
	return u
}

func (l *kindLab) httpClient(port string) *http.Client {
	return l.httpClientAddress("127.0.0.1:" + port)
}

func (l *kindLab) httpClientAddress(address string) *http.Client {
	l.t.Helper()
	ca, err := os.ReadFile(filepath.Join(l.state, "ca.crt"))
	if err != nil {
		l.t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		l.t.Fatal("invalid test CA")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}}
	l.t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 45 * time.Second}
}

func (l *kindLab) request(client *http.Client, user kindUser, method, path, body string, headers map[string]string, expected ...int) []byte {
	l.t.Helper()
	req, err := http.NewRequestWithContext(l.t.Context(), method, "https://worth.oopsbox.cn"+path, strings.NewReader(body))
	if err != nil {
		l.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://worth.oopsbox.cn")
	req.Header.Set("X-CSRF-Token", user.csrf)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if user.session != "" {
		req.AddCookie(&http.Cookie{Name: "__Host-human-worth-session", Value: user.session})
	}
	response, err := client.Do(req)
	if err != nil {
		l.t.Fatalf("deployment HTTPS %s %s failed: %v", method, path, err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 33<<20))
	if err != nil {
		l.t.Fatal("deployment response read failed")
	}
	matched := false
	for _, code := range expected {
		matched = matched || response.StatusCode == code
	}
	if !matched {
		var code struct {
			Code string `json:"code"`
		}
		// Deliberately do not log bodies, file bytes or credentials.
		_ = json.Unmarshal(data, &code)
		l.t.Fatalf("%s %s: got %d (%s), want one of %v; request_id=%s", method, path, response.StatusCode, code.Code, expected, response.Header.Get("X-Request-ID"))
	}
	if len(expected) > 1 {
		l.dependencyFailure(response.StatusCode, data)
	}
	return data
}

// A real dependency outage may refuse a connection or exhaust its deadline.
// Assert the matching reason as well as the two allowed HTTP statuses.
func (l *kindLab) dependencyFailure(code int, data []byte) {
	l.t.Helper()
	if err := dependencyFailure(code, data); err != nil {
		l.t.Fatal(err)
	}
}

// Port-forward only to loopback, using an OS-selected port. Process and logs
// are private and bounded. Logs survive t.Cleanup in the private validation
// state so a failed tunnel can be diagnosed before the outer driver cleans it.
func (l *kindLab) forward(resource string, remotePort int) string {
	l.t.Helper()
	log, err := os.CreateTemp(l.state, "forward-")
	if err != nil {
		l.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := l.command(ctx, "", "port-forward", "--address=127.0.0.1", resource, fmt.Sprintf(":%d", remotePort))
	cmd.Stdout, cmd.Stderr = log, log
	if err = cmd.Start(); err != nil {
		cancel()
		log.Close()
		l.t.Fatal("start loopback port-forward")
	}
	l.t.Cleanup(func() { cancel(); _ = cmd.Wait(); _ = log.Close() })
	pattern := regexp.MustCompile(`Forwarding from 127\.0\.0\.1:([0-9]+) ->`)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(log.Name())
		if match := pattern.FindSubmatch(data); len(match) == 2 {
			l.t.Logf("loopback port-forward %s:%d log=%s", resource, remotePort, log.Name())
			return string(match[1])
		}
		time.Sleep(100 * time.Millisecond)
	}
	l.t.Fatal("loopback port-forward did not start")
	return ""
}

// Agent Self-Claimed: recovery checkpoint AFTER an intentional dependency outage.
// A rollout or one pinned client's success does not establish that the normal
// Service and all long-lived headless gRPC clients have converged.
func (l *kindLab) waitGatewayEndpoints() {
	l.t.Helper()
	until := time.Now().Add(90 * time.Second)
	for {
		var endpoints struct {
			Items []struct {
				Endpoints []struct{ Conditions struct{ Ready *bool } }
			}
		}
		kindDecode(l.t, l.kube("", "get", "endpointslices", "-l", "kubernetes.io/service-name=gateway", "-o", "json"), &endpoints)
		ready := 0
		for _, slice := range endpoints.Items {
			for _, ep := range slice.Endpoints {
				if ep.Conditions.Ready != nil && *ep.Conditions.Ready {
					ready++
				}
			}
		}
		if ready == 2 {
			return
		}
		if time.Now().After(until) {
			l.t.Fatal("normal Gateway Service did not recover both endpoints after dependency outage")
		}
		time.Sleep(time.Second)
	}
}
