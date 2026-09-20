//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type draftResult struct {
	ID       string          `json:"id"`
	Author   string          `json:"authorId"`
	Revision int64           `json:"revision"`
	State    string          `json:"state"`
	Content  json.RawMessage `json:"content"`
}

// Reuse real Google-like HTTP OIDC + Identity + restricted database fixtures.
// Content is a separately executable OS process, not a mock or direct Go call.
func TestContentDraftHTTPGRPCPersistence(t *testing.T) {
	lab := newIdentityLab(t)
	ctx := t.Context()
	cfg := lab.db.Config().Copy()
	name := cfg.ConnConfig.Database
	owner, runtimeRole := name+"_content_owner", name+"_content_runtime"
	password := randomToken()
	_, err := lab.db.Exec(ctx, "CREATE ROLE "+pgx.Identifier{owner}.Sanitize()+" LOGIN PASSWORD '"+password+"'; CREATE ROLE "+pgx.Identifier{runtimeRole}.Sanitize()+" LOGIN PASSWORD '"+password+"'; CREATE SCHEMA content AUTHORIZATION "+pgx.Identifier{owner}.Sanitize())
	must(t, err)
	// Roles are global, so remove their owned schema before dropping them; the
	// outer fixture drops the complete disposable database afterwards.
	t.Cleanup(func() {
		lab.db.Exec(context.Background(), "DROP SCHEMA content CASCADE; DROP OWNED BY "+pgx.Identifier{runtimeRole}.Sanitize()+"; DROP OWNED BY "+pgx.Identifier{owner}.Sanitize())
		lab.admin.Exec(context.Background(), "DROP ROLE "+pgx.Identifier{runtimeRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{owner}.Sanitize())
	})
	cfg.ConnConfig.User, cfg.ConnConfig.Password = owner, password
	ownerDB, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	t.Cleanup(ownerDB.Close)
	must(t, content.Migrate(ctx, ownerDB))
	must(t, content.Migrate(ctx, ownerDB))
	for _, query := range []string{"SELECT * FROM identity.accounts", "CREATE SCHEMA unauthorized"} {
		if _, err := ownerDB.Exec(ctx, query); err == nil {
			t.Fatalf("content owner escaped its schema: %s", query)
		}
	}
	roleID := pgx.Identifier{runtimeRole}.Sanitize()
	_, err = lab.db.Exec(ctx, `GRANT USAGE ON SCHEMA content TO `+roleID+`;
 GRANT SELECT ON content.schema_migrations,content.task_drafts TO `+roleID+`;
 GRANT INSERT ON content.task_drafts TO `+roleID+`;
 GRANT UPDATE(content,revision,updated_at) ON content.task_drafts TO `+roleID+`;`)
	must(t, err)
	cfg.ConnConfig.User = runtimeRole
	runtimeDB, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	t.Cleanup(runtimeDB.Close)
	for _, query := range []string{"SELECT * FROM identity.accounts", "CREATE TABLE content.forbidden(id int)", "UPDATE content.task_drafts SET author_id='forged'", "UPDATE content.task_drafts SET state='published'", "DELETE FROM content.task_drafts"} {
		if _, err := runtimeDB.Exec(ctx, query); err == nil {
			t.Fatalf("content runtime overprivileged: %s", query)
		}
	}
	if err := content.Migrate(ctx, runtimeDB); err == nil {
		t.Fatal("runtime unexpectedly allowed DDL")
	}
	if _, err := lab.runtimeDB.Exec(ctx, "SELECT * FROM content.task_drafts"); err == nil {
		t.Fatal("Identity can read Content data")
	}

	binary := filepath.Join(t.TempDir(), "content")
	build := exec.CommandContext(ctx, "go", "build", "-race", "-o", binary, "../../cmd/content")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Content process: %v %s", err, output)
	}
	cert, key, ca := lab.pki.cert(t, "content")
	dir := t.TempDir()
	dsnFile := filepath.Join(dir, "database-url")
	dsn := &url.URL{Scheme: "postgres", User: url.UserPassword(runtimeRole, password), Host: net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port))), Path: name, RawQuery: "sslmode=disable"}
	must(t, os.WriteFile(dsnFile, []byte(dsn.String()), 0600))
	freeAddress := func() string {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		must(t, err)
		address := listener.Addr().String()
		must(t, listener.Close())
		return address
	}
	addresses := [2]string{freeAddress(), freeAddress()}
	var processes [2]*exec.Cmd
	var finished [2]chan error
	start := func(i int) {
		command := exec.Command(binary)
		command.Env = append(os.Environ(), "CONTENT_DATABASE_URL_FILE="+dsnFile, "IDENTITY_TARGET="+lab.addresses[i], "SERVICE_CERT_FILE="+cert, "SERVICE_KEY_FILE="+key, "SERVICE_CA_FILE="+ca, "GRPC_LISTEN="+addresses[i], "HEALTH_LISTEN="+freeAddress())
		// Do not accidentally inherit a caller's tracing exporter or secrets.
		command.Env = append(command.Env, "OTEL_EXPORTER_OTLP_ENDPOINT=")
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		must(t, command.Start())
		processes[i] = command
		finished[i] = make(chan error, 1)
		go func() { finished[i] <- command.Wait() }()
	}
	stop := func(i int) {
		if processes[i] == nil {
			return
		}
		processes[i].Process.Signal(os.Interrupt)
		select {
		case err := <-finished[i]:
			if err != nil {
				t.Errorf("content process exited: %v", err)
			}
		case <-time.After(12 * time.Second):
			processes[i].Process.Kill()
			<-finished[i]
			t.Error("content did not stop gracefully")
		}
		processes[i] = nil
	}
	for i := range processes {
		start(i)
		t.Cleanup(func() { stop(i) })
	}
	dial := func(i int, caller string, pki *testPKI) *grpc.ClientConn {
		c, k, a := pki.cert(t, caller)
		tls, err := platform.TLS(c, k, a, "content")
		must(t, err)
		conn, err := grpc.NewClient(addresses[i], grpc.WithTransportCredentials(credentials.NewTLS(tls)), grpc.WithDisableRetry())
		must(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}
	var clients [2]contentpb.ContentServiceClient
	for i := range clients {
		clients[i] = contentpb.NewContentServiceClient(dial(i, "gateway", lab.pki))
	}
	// Separate HTTP gateways connected to different Content replicas. The same
	// fixed public Origin is used by Identity's CSRF checks.
	var web [2]*httptest.Server
	for i := range web {
		handler, err := gateway.New(lab.clients[i], gateway.Options{Origin: lab.origin, Content: clients[i], Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		must(t, err)
		web[i] = httptest.NewTLSServer(handler)
		t.Cleanup(web[i].Close)
	}
	alice, aliceSession := lab.login(t, "content-alice", nil)
	bob, bobSession := lab.login(t, "content-bob", nil)
	// An administrator is not a bypass for another author's private draft.
	_, err = lab.db.Exec(ctx, "UPDATE identity.accounts SET role='admin',auth_version=auth_version+1 WHERE id=$1", bobSession.Account.Id)
	must(t, err)
	bob, bobSession = lab.login(t, "content-bob", bob)
	if bobSession.Role != pb.Role_ROLE_ADMIN {
		t.Fatal("admin fixture failed")
	}
	request := func(replica int, method, path, body string, session *http.Cookie, csrf, key string) *http.Response {
		req, err := http.NewRequestWithContext(ctx, method, web[replica].URL+path, strings.NewReader(body))
		must(t, err)
		req.Host = strings.TrimPrefix(lab.origin, "https://")
		if session != nil {
			req.AddCookie(session)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", lab.origin)
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response, err := web[replica].Client().Do(req)
		must(t, err)
		return response
	}
	const initial = `{"title":"private draft","summary":"summary","description":"body","entries":[]}`
	const createKey = "content-create-key-0001"
	// Wait for an actual Content RPC to reach its auth check, not merely a TCP port.
	deadline := time.Now().Add(10 * time.Second)
	for {
		attempt, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		_, err = clients[0].GetMyTaskSubmission(attempt, &contentpb.GetMyTaskSubmissionRequest{})
		cancel()
		if status.Code(err) == codes.Unauthenticated {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("content not ready: %v", err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	check := func(response *http.Response, want int) { httpStatus(t, response, want); response.Body.Close() }
	check(request(0, "POST", "/api/tasks", initial, nil, "", createKey), 401)
	check(request(0, "POST", "/api/tasks", initial, alice, "", createKey), 403)
	check(request(0, "POST", "/api/tasks", initial, alice, aliceSession.CsrfToken, ""), 400)
	check(request(0, "POST", "/api/tasks", initial, alice, aliceSession.CsrfToken, "short"), 400)
	check(request(0, "POST", "/api/tasks", strings.Replace(initial, "private draft", strings.Repeat("x", 20000), 1), alice, aliceSession.CsrfToken, createKey), 413)
	check(request(0, "POST", "/api/tasks", strings.Replace(initial, "private draft", strings.Repeat("x", 12500), 1), alice, aliceSession.CsrfToken, createKey), 400)
	// Private draft APIs reject a real MCP credential, even for a logged-in owner.
	tokenResponse := lab.request(t, 0, "POST", "/api/me/mcp-tokens", []*http.Cookie{alice}, `{"name":"Content denial test","createRequestId":"content-token-request-001"}`, map[string]string{"Origin": lab.origin, "X-CSRF-Token": aliceSession.CsrfToken, "Content-Type": "application/json"})
	httpStatus(t, tokenResponse, 201)
	var token struct {
		Token string `json:"token"`
	}
	readJSON(t, tokenResponse, &token)
	for _, method := range []string{"POST", "GET"} {
		path := "/api/tasks"
		if method == "GET" {
			path = "/api/me/tasks/tsk_00000000000000000000000000000000"
		}
		req, err := http.NewRequestWithContext(ctx, method, web[0].URL+path, strings.NewReader(initial))
		must(t, err)
		req.Host = strings.TrimPrefix(lab.origin, "https://")
		req.Header.Set("Authorization", "Bearer "+token.Token)
		response, err := web[0].Client().Do(req)
		must(t, err)
		check(response, 403)
	}
	for _, body := range []string{
		`{"title":"forged","summary":"","description":"","entries":[],"authorId":"` + bobSession.Account.Id + `"}`,
		`{"title":"forged","summary":"","description":"","entries":[],"state":"published"}`,
		`{"title":"missing","summary":"","description":""}`,
		`{"title":"null entries","summary":"","description":"","entries":null}`,
		`{"title":"NUL\u0000","summary":"","description":"","entries":[]}`,
	} {
		check(request(0, "POST", "/api/tasks", body, alice, aliceSession.CsrfToken, createKey), 400)
	}

	// Identical concurrent creates on independent processes converge to one row.
	results := make(chan draftResult, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := request(i, "POST", "/api/tasks", initial, alice, aliceSession.CsrfToken, createKey)
			httpStatus(t, r, 201)
			var result draftResult
			readJSON(t, r, &result)
			results <- result
		}(i)
	}
	wg.Wait()
	close(results)
	var draft draftResult
	for result := range results {
		if draft.ID != "" && result.ID != draft.ID {
			t.Fatal("duplicate task created")
		}
		draft = result
	}
	if draft.ID == "" || draft.Author != aliceSession.Account.Id || draft.State != "draft" || draft.Revision != 1 {
		t.Fatalf("wrong draft: %+v", draft)
	}
	path := "/api/me/tasks/" + draft.ID
	check(request(0, "GET", path, "", nil, "", ""), 401)
	check(request(1, "GET", path, "", bob, "", ""), 404)
	check(request(1, "PUT", path, `{"expectedRevision":1,"content":`+initial+`}`, bob, bobSession.CsrfToken, ""), 404)
	check(request(0, "GET", "/api/tasks/"+draft.ID, "", alice, "", ""), 404)
	check(request(0, "GET", "/api/tasks", "", alice, "", ""), 405)
	check(request(0, "POST", "/api/tasks", strings.Replace(initial, "private draft", "different", 1), alice, aliceSession.CsrfToken, createKey), 409)
	// Same key belongs to another author's namespace, without access to Alice's row.
	response := request(1, "POST", "/api/tasks", initial, bob, bobSession.CsrfToken, createKey)
	httpStatus(t, response, 201)
	var bobDraft draftResult
	readJSON(t, response, &bobDraft)
	if bobDraft.ID == draft.ID || bobDraft.Author != bobSession.Account.Id {
		t.Fatal("idempotency key crossed accounts")
	}

	// Force a real database lock wait through HTTP. A deadline is not permission
	// to blindly overwrite; after releasing the lock, confirm no revision changed.
	blocked, err := lab.db.Begin(ctx)
	must(t, err)
	defer blocked.Rollback(context.Background())
	_, err = blocked.Exec(ctx, "SELECT id FROM content.task_drafts WHERE id=$1 FOR UPDATE", draft.ID)
	must(t, err)
	check(request(0, "PUT", path, `{"expectedRevision":1,"content":`+initial+`}`, alice, aliceSession.CsrfToken, ""), 503)
	must(t, blocked.Rollback(ctx))
	var unchanged int64
	must(t, lab.db.QueryRow(ctx, "SELECT revision FROM content.task_drafts WHERE id=$1", draft.ID).Scan(&unchanged))
	if unchanged != 1 {
		t.Fatal("timed out lock waiter wrote a revision")
	}
	// Missing Identity is not the only dependency failure: do not fake-success
	// file inputs before Asset can validate ownership.
	fileBody := `{"title":"file","summary":"","description":"","entries":[{"side":"human","title":"h","source":"s","artifacts":[{"kind":"file","assetId":"unverified"}],"permissions":{"cloudUse":false}}]}`
	check(request(0, "POST", "/api/tasks", fileBody, alice, aliceSession.CsrfToken, "content-file-rejected-01"), 409)
	// Store both kinds of initial draft with explicit cloud authorization choices.
	const entries = `{"title":"with entries","summary":"","description":"","category":null,"entries":[{"side":"human","title":"human work","source":"my work","artifacts":[{"kind":"link","url":"https://example.com/human"}],"permissions":{"cloudUse":false}},{"side":"agent","title":"agent work","source":"model output","artifacts":[{"kind":"link","url":"https://example.com/agent"}],"permissions":{"cloudUse":true},"agentConfiguration":{"model":"draft-model","parameters":{"temperature":0.5}}}]}`
	response = request(0, "PUT", path, `{"expectedRevision":1,"content":`+entries+`}`, alice, aliceSession.CsrfToken, "")
	httpStatus(t, response, 200)
	readJSON(t, response, &draft)
	if draft.Revision != 2 || !strings.Contains(string(draft.Content), "cloudUse") {
		t.Fatal("entries not persisted")
	}
	// Different edits with the same revision cannot overwrite one another.
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := strings.Replace(initial, "private draft", []string{"edit A", "edit B"}[i], 1)
			r := request(i, "PUT", path, `{"expectedRevision":2,"content":`+body+`}`, alice, aliceSession.CsrfToken, "")
			statuses <- r.StatusCode
			r.Body.Close()
		}(i)
	}
	wg.Wait()
	close(statuses)
	counts := map[int]int{}
	for code := range statuses {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("concurrent edits: %v", counts)
	}
	check(request(0, "PUT", path, `{"expectedRevision":2,"content":`+initial+`}`, alice, aliceSession.CsrfToken, ""), 409)
	response = request(1, "GET", path, "", alice, "", "")
	httpStatus(t, response, 200)
	readJSON(t, response, &draft)
	if draft.Revision != 3 {
		t.Fatal("lost update/retry incremented revision")
	}
	// Create retry after editing returns current snapshot, not initial content.
	response = request(0, "POST", "/api/tasks", initial, alice, aliceSession.CsrfToken, createKey)
	httpStatus(t, response, 201)
	var replay draftResult
	readJSON(t, response, &replay)
	if replay.ID != draft.ID || replay.Revision != 3 {
		t.Fatal("replay reset draft")
	}

	stop(0)
	start(0)
	deadline = time.Now().Add(10 * time.Second)
	for {
		response = request(0, "GET", path, "", alice, "", "")
		if response.StatusCode == 200 {
			break
		}
		response.Body.Close()
		if time.Now().After(deadline) {
			t.Fatal("restarted Content never recovered")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var persisted draftResult
	readJSON(t, response, &persisted)
	var before, after any
	must(t, json.Unmarshal(draft.Content, &before))
	must(t, json.Unmarshal(persisted.Content, &after))
	if persisted.ID != draft.ID || persisted.Revision != 3 || !reflect.DeepEqual(before, after) {
		t.Fatal("restart lost draft")
	}

	// mTLS identifies the caller; a valid assertion does not grant other services access.
	actor, err := lab.clients[0].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: alice.Value}, Audience: "content", FullMethod: contentpb.ContentService_GetMyTaskSubmission_FullMethodName})
	must(t, err)
	_, err = contentpb.NewContentServiceClient(dial(0, "asset", lab.pki)).GetMyTaskSubmission(ctx, &contentpb.GetMyTaskSubmissionRequest{ActorAssertion: actor.ActorAssertion, TaskId: draft.ID})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong service allowed: %v", err)
	}
	wrongCA, cancel := context.WithTimeout(ctx, time.Second)
	_, err = contentpb.NewContentServiceClient(dial(0, "gateway", newTestPKI(t))).GetMyTaskSubmission(wrongCA, &contentpb.GetMyTaskSubmissionRequest{ActorAssertion: actor.ActorAssertion, TaskId: draft.ID})
	cancel()
	if err == nil {
		t.Fatal("untrusted certificate allowed")
	}
	_, err = clients[0].ReplaceTaskDraft(ctx, &contentpb.ReplaceTaskDraftRequest{ActorAssertion: actor.ActorAssertion, TaskId: draft.ID, ExpectedRevision: 3})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("assertion crossed methods: %v", err)
	}
	// RPC boundary rejects invalid content even when bypassing HTTP validation.
	writeActor, err := lab.clients[0].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: alice.Value}, Audience: "content", FullMethod: contentpb.ContentService_ReplaceTaskDraft_FullMethodName, Origin: lab.origin, CsrfToken: aliceSession.CsrfToken})
	must(t, err)
	_, err = clients[0].ReplaceTaskDraft(ctx, &contentpb.ReplaceTaskDraftRequest{ActorAssertion: writeActor.ActorAssertion, TaskId: draft.ID, ExpectedRevision: 3})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("RPC invalid content accepted: %v", err)
	}
	check(lab.request(t, 0, "POST", "/api/auth/logout", []*http.Cookie{alice}, "", map[string]string{"Origin": lab.origin, "X-CSRF-Token": aliceSession.CsrfToken}), 204)
	check(request(0, "GET", path, "", alice, "", ""), 401)
	check(request(0, "POST", "/api/tasks", initial, alice, aliceSession.CsrfToken, createKey), 401)
	_, err = clients[0].GetMyTaskSubmission(ctx, &contentpb.GetMyTaskSubmissionRequest{ActorAssertion: actor.ActorAssertion, TaskId: draft.ID})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("pre-revocation assertion survived: %v", err)
	}
	var input contentpb.TaskDraftInput
	must(t, protojson.Unmarshal([]byte(initial), &input))
	_, err = clients[0].ReplaceTaskDraft(ctx, &contentpb.ReplaceTaskDraftRequest{ActorAssertion: writeActor.ActorAssertion, TaskId: draft.ID, ExpectedRevision: 3, Content: &input})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("revoked writer survived: %v", err)
	}

	// VerifyActor must fail closed independently of gateway authentication.
	bobActor, err := lab.clients[1].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: bob.Value}, Audience: "content", FullMethod: contentpb.ContentService_GetMyTaskSubmission_FullMethodName})
	must(t, err)
	lab.grpc[0].Stop()
	_, err = clients[0].GetMyTaskSubmission(ctx, &contentpb.GetMyTaskSubmissionRequest{ActorAssertion: bobActor.ActorAssertion, TaskId: bobDraft.ID})
	if err == nil {
		t.Fatal("Content continued when its Identity was unavailable")
	}
	check(request(1, "GET", "/api/me/tasks/"+bobDraft.ID, "", bob, "", ""), 200)
	var count int
	must(t, lab.db.QueryRow(ctx, "SELECT count(*) FROM content.task_drafts").Scan(&count))
	if count != 2 {
		t.Fatalf("denied/retried requests created rows: %d", count)
	}
	must(t, lab.db.QueryRow(ctx, "SELECT count(*) FROM content.task_drafts WHERE state <> 'draft'").Scan(&count))
	if count != 0 {
		t.Fatal("draft automatically published")
	}
	t.Log("PASS: real HTTPS → gateway → mTLS Content process → VerifyActor → restricted PostgreSQL; two replicas, concurrent writes, process restart and refusal paths")
}
