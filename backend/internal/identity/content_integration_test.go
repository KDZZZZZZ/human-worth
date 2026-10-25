//go:build integration

package identity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	"sort"
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type draftResult struct {
	Kind            string          `json:"kind"`
	ID              string          `json:"id"`
	Author          string          `json:"authorId"`
	Revision        int64           `json:"revision"`
	State           string          `json:"state"`
	Content         json.RawMessage `json:"content"`
	ReviewID        *string         `json:"reviewId"`
	RejectionReason *string         `json:"rejectionReason"`
}
type submissionPage struct {
	Items      []draftResult `json:"items"`
	NextCursor *string       `json:"nextCursor"`
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
	// Reproduce an upgrade from the released 001 schema with data that the old
	// aggregate-size validation accepted. The list migration must not index the
	// complete category value and fail before the service can become ready.
	initialMigration, err := os.ReadFile(filepath.Join("..", "content", "migrations", "001_content.sql"))
	must(t, err)
	_, err = ownerDB.Exec(ctx, `CREATE TABLE content.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`)
	must(t, err)
	_, err = ownerDB.Exec(ctx, string(initialMigration))
	must(t, err)
	initialChecksum := sha256.Sum256(initialMigration)
	_, err = ownerDB.Exec(ctx, `INSERT INTO content.schema_migrations(version,checksum) VALUES('001_content.sql',$1)`, fmt.Sprintf("%x", initialChecksum))
	must(t, err)
	legacyCategory := strings.Repeat("绘", 2100)
	var hardCategory strings.Builder
	for i := 0; hardCategory.Len() < 6400; i++ {
		digest := sha256.Sum256([]byte(strconv.Itoa(i)))
		fmt.Fprintf(&hardCategory, "%x", digest)
	}
	hardLegacyCategory := hardCategory.String()[:6400]
	legacyRows := []struct {
		id, key, category string
	}{
		{"tsk_ffffffffffffffffffffffffffffffff", "legacy-upgrade-key-0001", legacyCategory},
		{"tsk_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "legacy-upgrade-key-0002", hardLegacyCategory},
	}
	for _, row := range legacyRows {
		legacyContent, marshalErr := json.Marshal(map[string]any{"title": "legacy", "summary": "", "description": "", "category": row.category, "entries": []any{}})
		must(t, marshalErr)
		if len(legacyContent) >= 12000 {
			t.Fatalf("legacy upgrade fixture is not old-version legal: %d bytes", len(legacyContent))
		}
		legacyHash := sha256.Sum256(legacyContent)
		_, err = ownerDB.Exec(ctx, `INSERT INTO content.task_drafts(id,author_id,create_key,create_hash,content) VALUES($1,'legacy-upgrade-account',$2,$3,$4)`, row.id, row.key, legacyHash[:], legacyContent)
		must(t, err)
	}
	_, err = ownerDB.Exec(ctx, `CREATE INDEX legacy_full_category_index_probe ON content.task_drafts(author_id,(content->>'category'),created_at DESC,id DESC) WHERE state='draft'`)
	var indexError *pgconn.PgError
	if err == nil || !errors.As(err, &indexError) || indexError.Code != "54000" {
		t.Fatalf("legacy full-category index did not fail with an oversized entry: %v", err)
	}
	must(t, content.Migrate(ctx, ownerDB))
	must(t, content.Migrate(ctx, ownerDB))
	var migrationCount, indexCount int
	must(t, ownerDB.QueryRow(ctx, `SELECT count(*) FROM content.schema_migrations`).Scan(&migrationCount))
	must(t, ownerDB.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='content' AND indexname='task_drafts_author_created_id'`).Scan(&indexCount))
	if migrationCount != 8 || indexCount != 1 {
		t.Fatalf("Content list migration missing: migrations=%d indexes=%d", migrationCount, indexCount)
	}
	for _, row := range legacyRows {
		var migratedCategory string
		must(t, ownerDB.QueryRow(ctx, `SELECT content->>'category' FROM content.task_drafts WHERE id=$1`, row.id).Scan(&migratedCategory))
		if migratedCategory != row.category {
			t.Fatalf("list migration changed legacy category %s", row.id)
		}
	}
	_, err = ownerDB.Exec(ctx, `DELETE FROM content.task_drafts WHERE author_id='legacy-upgrade-account'`)
	must(t, err)
	for _, query := range []string{"SELECT * FROM identity.accounts", "CREATE SCHEMA unauthorized"} {
		if _, err := ownerDB.Exec(ctx, query); err == nil {
			t.Fatalf("content owner escaped its schema: %s", query)
		}
	}
	serviceTestGrants(t, lab, "content", pgx.Identifier{runtimeRole}.Sanitize(), "../../../ops/content/grants.sql")
	cfg.ConnConfig.User = runtimeRole
	runtimeDB, err := pgxpool.NewWithConfig(ctx, cfg)
	must(t, err)
	t.Cleanup(runtimeDB.Close)
	for _, query := range []string{"SELECT * FROM identity.accounts", "CREATE TABLE content.forbidden(id int)", "UPDATE content.task_drafts SET author_id='forged'", "UPDATE content.task_drafts SET create_key='forged'", "DELETE FROM content.task_drafts"} {
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
	// The merged gateway must retain deployment metadata and private list routes.
	deploymentFile := filepath.Join(t.TempDir(), "deployment-status.json")
	deploymentRevision := strings.Repeat("a", 40)
	must(t, os.WriteFile(deploymentFile, []byte(`{"revision":"`+deploymentRevision+`","services":["identity","content","gateway"]}`), 0600))
	var web [2]*httptest.Server
	for i := range web {
		handler, err := gateway.New(lab.clients[i], gateway.Options{Origin: lab.origin, Content: clients[i], DeploymentStatusFile: deploymentFile, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
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
	page := func(path string, session *http.Cookie) submissionPage {
		response := request(0, "GET", path, "", session, "", "")
		if response.StatusCode != 200 {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			t.Fatalf("GET %s: HTTP status=%d want=200 response=%s", path, response.StatusCode, body)
		}
		var result submissionPage
		readJSON(t, response, &result)
		return result
	}
	for i := range web {
		health := request(i, "GET", "/api/health", "", nil, "", "")
		httpStatus(t, health, 200)
		var status struct {
			Revision string   `json:"deploymentRevision"`
			Services []string `json:"deployedServices"`
		}
		readJSON(t, health, &status)
		if status.Revision != deploymentRevision || !reflect.DeepEqual(status.Services, []string{"identity", "content", "gateway"}) {
			t.Fatalf("merged gateway lost deployment metadata: %+v", status)
		}
	}
	check(request(0, "GET", "/api/me/submissions", "", nil, "", ""), 401)
	emptyPage := page("/api/me/submissions", alice)
	if len(emptyPage.Items) != 0 || emptyPage.NextCursor != nil {
		t.Fatalf("non-empty initial list: %+v", emptyPage)
	}
	for _, path := range []string{
		"/api/me/submissions?kind=cloud",
		"/api/me/submissions?kind=",
		"/api/me/submissions?state=unsupported",
		"/api/me/submissions?state=",
		"/api/me/submissions?category=",
		"/api/me/submissions?category=" + strings.Repeat("a", 12001),
		"/api/me/submissions?limit=0",
		"/api/me/submissions?limit=101",
		"/api/me/submissions?limit=nope",
		"/api/me/submissions?limit=1&limit=2",
		"/api/me/submissions?category=a&category=b",
		"/api/me/submissions?unknown=value",
		"/api/me/submissions?cursor=" + strings.Repeat("a", 2049),
	} {
		check(request(0, "GET", path, "", alice, "", ""), 400)
	}
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
	req, err := http.NewRequestWithContext(ctx, "GET", web[0].URL+"/api/me/submissions", nil)
	must(t, err)
	req.Host = strings.TrimPrefix(lab.origin, "https://")
	req.Header.Set("Authorization", "Bearer "+token.Token)
	response, err := web[0].Client().Do(req)
	must(t, err)
	check(response, 403)
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
	check(request(0, "GET", "/api/tasks", "", alice, "", ""), 503)
	check(request(0, "POST", "/api/tasks", strings.Replace(initial, "private draft", "different", 1), alice, aliceSession.CsrfToken, createKey), 409)
	// Same key belongs to another author's namespace, without access to Alice's row.
	response = request(1, "POST", "/api/tasks", initial, bob, bobSession.CsrfToken, createKey)
	httpStatus(t, response, 201)
	var bobDraft draftResult
	readJSON(t, response, &bobDraft)
	if bobDraft.ID == draft.ID || bobDraft.Author != bobSession.Account.Id {
		t.Fatal("idempotency key crossed accounts")
	}

	createExtra := func(replica int, body, key string) draftResult {
		response := request(replica, "POST", "/api/tasks", body, alice, aliceSession.CsrfToken, key)
		httpStatus(t, response, 201)
		var result draftResult
		readJSON(t, response, &result)
		return result
	}
	drawingOne := createExtra(0, `{"title":"drawing one","summary":"","description":"","category":"绘画","entries":[]}`, "content-list-drawing-01")
	drawingTwo := createExtra(1, `{"title":"drawing two","summary":"","description":"","category":"绘画","entries":[]}`, "content-list-drawing-02")
	drawingThree := createExtra(0, `{"title":"drawing three","summary":"","description":"","category":"绘画","entries":[]}`, "content-list-drawing-03")
	codeDraft := createExtra(1, `{"title":"code draft","summary":"","description":"","category":"代码","entries":[]}`, "content-list-code-0001")
	fixedCreated := time.Date(2026, 9, 20, 12, 34, 56, 123456000, time.UTC)
	_, err = lab.db.Exec(ctx, `UPDATE content.task_drafts SET created_at=$1 WHERE author_id=$2`, fixedCreated, aliceSession.Account.Id)
	must(t, err)

	expectedIDs := []string{draft.ID, drawingOne.ID, drawingTwo.ID, drawingThree.ID, codeDraft.ID}
	sort.Sort(sort.Reverse(sort.StringSlice(expectedIDs)))
	ids := func(items []draftResult) []string {
		result := make([]string, 0, len(items))
		for _, item := range items {
			result = append(result, item.ID)
		}
		return result
	}
	fullPage := page("/api/me/submissions?limit=100", alice)
	if fullPage.NextCursor != nil || !reflect.DeepEqual(ids(fullPage.Items), expectedIDs) {
		t.Fatalf("full list mismatch: ids=%v cursor=%v want=%v", ids(fullPage.Items), fullPage.NextCursor, expectedIDs)
	}
	for _, item := range fullPage.Items {
		if item.Kind != "task" || item.Author != aliceSession.Account.Id || item.State != "draft" || item.Revision < 1 || len(item.Content) == 0 || item.ReviewID != nil || item.RejectionReason != nil {
			t.Fatalf("incomplete list item: %+v", item)
		}
	}
	// Add a temporary row-level policy that sleeps once per list statement. This
	// delays the real PostgreSQL query beyond the ordinary two-second RPC budget
	// without changing the production lock_timeout or widening other RPCs.
	const delayPolicy = `
CREATE FUNCTION content.test_delay_task_drafts() RETURNS boolean
LANGUAGE plpgsql VOLATILE AS $$
BEGIN
    IF current_setting('humanworth.test_delay_done', true) IS DISTINCT FROM '1' THEN
        PERFORM set_config('humanworth.test_delay_done', '1', true);
        PERFORM pg_sleep(2.5);
    END IF;
    RETURN true;
END
$$;
ALTER TABLE content.task_drafts ENABLE ROW LEVEL SECURITY;
CREATE POLICY test_delay_task_drafts ON content.task_drafts FOR SELECT TO PUBLIC
USING (content.test_delay_task_drafts());`
	const removeDelayPolicy = `
DROP POLICY IF EXISTS test_delay_task_drafts ON content.task_drafts;
ALTER TABLE content.task_drafts DISABLE ROW LEVEL SECURITY;
DROP FUNCTION IF EXISTS content.test_delay_task_drafts();`
	_, err = ownerDB.Exec(ctx, delayPolicy)
	must(t, err)
	delayRemoved := false
	defer func() {
		if !delayRemoved {
			ownerDB.Exec(context.Background(), removeDelayPolicy)
		}
	}()
	delayBegan := time.Now()
	delayedResponse := request(0, "GET", "/api/me/submissions?limit=100", "", alice, "", "")
	elapsed := time.Since(delayBegan)
	_, removeErr := ownerDB.Exec(ctx, removeDelayPolicy)
	must(t, removeErr)
	delayRemoved = true
	if delayedResponse.StatusCode != 200 {
		body, _ := io.ReadAll(delayedResponse.Body)
		delayedResponse.Body.Close()
		t.Fatalf("list failed after controlled database delay: status=%d elapsed=%v response=%s", delayedResponse.StatusCode, elapsed, body)
	}
	delayedResponse.Body.Close()
	if elapsed < 2500*time.Millisecond || elapsed >= 5*time.Second {
		t.Fatalf("list deadline was not end-to-end: elapsed=%v", elapsed)
	}
	equalPage := page("/api/me/submissions?limit=5", alice)
	if len(equalPage.Items) != 5 || equalPage.NextCursor != nil {
		t.Fatalf("exact limit page mismatch: %+v", equalPage)
	}
	firstPage := page("/api/me/submissions?limit=2", alice)
	if len(firstPage.Items) != 2 || firstPage.NextCursor == nil {
		t.Fatalf("first page mismatch: %+v", firstPage)
	}
	emptyCursorPage := page("/api/me/submissions?cursor=&limit=2", alice)
	if !reflect.DeepEqual(ids(firstPage.Items), ids(emptyCursorPage.Items)) {
		t.Fatalf("empty cursor changed first page: %v vs %v", ids(firstPage.Items), ids(emptyCursorPage.Items))
	}
	collect := func(values url.Values, session *http.Cookie) []string {
		seen := map[string]bool{}
		var result []string
		pageNumber := 0
		for {
			current := page("/api/me/submissions?"+values.Encode(), session)
			for _, item := range current.Items {
				if seen[item.ID] {
					t.Fatalf("duplicate across pages: %s", item.ID)
				}
				seen[item.ID] = true
				result = append(result, item.ID)
			}
			if current.NextCursor == nil {
				return result
			}
			values.Set("cursor", *current.NextCursor)
			pageNumber++
			if pageNumber == 1 {
				// Omitted and explicit first-phase values are the same effective filter.
				values.Set("kind", "task")
				values.Set("state", "draft")
			}
			if pageNumber > 10 {
				t.Fatal("pagination did not terminate")
			}
		}
	}
	if got := collect(url.Values{"limit": {"2"}}, alice); !reflect.DeepEqual(got, expectedIDs) {
		t.Fatalf("static pagination mismatch: got=%v want=%v", got, expectedIDs)
	}
	cursorQuery := url.Values{"limit": {"2"}, "cursor": {*firstPage.NextCursor}}
	check(request(0, "GET", "/api/me/submissions?"+cursorQuery.Encode(), "", bob, "", ""), 400)
	cursorQuery.Set("category", "绘画")
	check(request(0, "GET", "/api/me/submissions?"+cursorQuery.Encode(), "", alice, "", ""), 400)
	cursorQuery = url.Values{"cursor": {*firstPage.NextCursor + "!"}}
	check(request(0, "GET", "/api/me/submissions?"+cursorQuery.Encode(), "", alice, "", ""), 400)
	decodedCursor, err := base64.RawURLEncoding.DecodeString(*firstPage.NextCursor)
	must(t, err)
	var cursorPayload map[string]any
	must(t, json.Unmarshal(decodedCursor, &cursorPayload))
	cursorPayload["v"] = float64(2)
	decodedCursor, err = json.Marshal(cursorPayload)
	must(t, err)
	cursorQuery = url.Values{"cursor": {base64.RawURLEncoding.EncodeToString(decodedCursor)}}
	check(request(0, "GET", "/api/me/submissions?"+cursorQuery.Encode(), "", alice, "", ""), 400)

	expectedDrawing := []string{drawingOne.ID, drawingTwo.ID, drawingThree.ID}
	sort.Sort(sort.Reverse(sort.StringSlice(expectedDrawing)))
	drawingValues := url.Values{"category": {"绘画"}, "limit": {"2"}}
	drawingFirst := page("/api/me/submissions?"+drawingValues.Encode(), alice)
	if len(drawingFirst.Items) != 2 || drawingFirst.NextCursor == nil {
		t.Fatalf("category first page mismatch: %+v", drawingFirst)
	}
	if got := collect(url.Values{"category": {"绘画"}, "limit": {"2"}}, alice); !reflect.DeepEqual(got, expectedDrawing) {
		t.Fatalf("category pagination mismatch: got=%v want=%v", got, expectedDrawing)
	}
	unmatched := page("/api/me/submissions?"+url.Values{"category": {"绘畫"}}.Encode(), alice)
	if len(unmatched.Items) != 0 || unmatched.NextCursor != nil {
		t.Fatalf("unmatched category was not empty: %+v", unmatched)
	}
	bobPage := page("/api/me/submissions?limit=100", bob)
	if len(bobPage.Items) != 1 || bobPage.Items[0].ID != bobDraft.ID || bobPage.Items[0].Author != bobSession.Account.Id {
		t.Fatalf("admin list crossed authors: %+v", bobPage)
	}

	// There is no cross-request snapshot. Changing category leaves created_at
	// unchanged but removes the item from the old filter and adds it to the new one.
	movingID := expectedDrawing[len(expectedDrawing)-1]
	var movingCreated time.Time
	must(t, lab.db.QueryRow(ctx, `SELECT created_at FROM content.task_drafts WHERE id=$1`, movingID).Scan(&movingCreated))
	movedBody := `{"title":"moved drawing","summary":"","description":"","category":"代码","entries":[]}`
	response = request(0, "PUT", "/api/me/tasks/"+movingID, `{"expectedRevision":1,"content":`+movedBody+`}`, alice, aliceSession.CsrfToken, "")
	httpStatus(t, response, 200)
	response.Body.Close()
	var movingCreatedAfter time.Time
	must(t, lab.db.QueryRow(ctx, `SELECT created_at FROM content.task_drafts WHERE id=$1`, movingID).Scan(&movingCreatedAfter))
	if !movingCreated.Equal(movingCreatedAfter) {
		t.Fatalf("edit changed created_at: before=%v after=%v", movingCreated, movingCreatedAfter)
	}
	drawingValues.Set("cursor", *drawingFirst.NextCursor)
	oldSecondPage := page("/api/me/submissions?"+drawingValues.Encode(), alice)
	if len(oldSecondPage.Items) != 0 || oldSecondPage.NextCursor != nil {
		t.Fatalf("mutable filter unexpectedly kept moved item: %+v", oldSecondPage)
	}
	freshDrawing := page("/api/me/submissions?"+url.Values{"category": {"绘画"}, "limit": {"100"}}.Encode(), alice)
	if len(freshDrawing.Items) != 2 {
		t.Fatalf("fresh drawing filter mismatch: %+v", freshDrawing)
	}
	freshCode := page("/api/me/submissions?"+url.Values{"category": {"代码"}, "limit": {"100"}}.Encode(), alice)
	if len(freshCode.Items) != 2 {
		t.Fatalf("fresh code filter mismatch: %+v", freshCode)
	}

	// Long categories accepted by the released draft implementation remain
	// writable, idempotently replayable, and exactly filterable after upgrade.
	charlie, charlieSession := lab.login(t, "content-large-page", nil)
	legacyBody, err := json.Marshal(map[string]any{"title": "legacy category", "summary": "", "description": "", "category": legacyCategory, "entries": []any{}})
	must(t, err)
	response = request(0, "POST", "/api/tasks", string(legacyBody), charlie, charlieSession.CsrfToken, "legacy-category-key-0001")
	httpStatus(t, response, 201)
	var legacyDraft draftResult
	readJSON(t, response, &legacyDraft)
	response = request(0, "POST", "/api/tasks", string(legacyBody), charlie, charlieSession.CsrfToken, "legacy-category-key-0001")
	httpStatus(t, response, 201)
	var legacyReplay draftResult
	readJSON(t, response, &legacyReplay)
	if legacyReplay.ID != legacyDraft.ID {
		t.Fatal("long-category idempotency replay changed task")
	}
	legacyPage := page("/api/me/submissions?"+url.Values{"category": {legacyCategory}}.Encode(), charlie)
	if len(legacyPage.Items) != 1 || legacyPage.Items[0].ID != legacyDraft.ID || legacyPage.NextCursor != nil {
		t.Fatalf("long-category filter mismatch: %+v", legacyPage)
	}
	_, err = lab.db.Exec(ctx, `DELETE FROM content.task_drafts WHERE author_id=$1`, charlieSession.Account.Id)
	must(t, err)

	// Struct numbers are much larger in protobuf than in JSON. Create one through
	// HTTP, prepare 100 legal rows, and verify the real HTTP -> gRPC -> PostgreSQL
	// path splits by encoded response bytes without truncating a submission.
	numbers := make([]int, 3000)
	for i := range numbers {
		numbers[i] = i % 10
	}
	largeContent, err := json.Marshal(map[string]any{
		"title": "large", "summary": "", "description": "", "entries": []any{map[string]any{
			"side": "agent", "title": "work", "source": "model",
			"artifacts":          []any{map[string]any{"kind": "link", "url": "https://example.com/work"}},
			"permissions":        map[string]any{"cloudUse": false},
			"agentConfiguration": map[string]any{"model": "example", "parameters": map[string]any{"nested": map[string]any{"values": numbers}}},
		}},
	})
	must(t, err)
	if len(largeContent) >= 12000 {
		t.Fatalf("large page fixture exceeds legal JSON size: %d", len(largeContent))
	}
	response = request(0, "POST", "/api/tasks", string(largeContent), charlie, charlieSession.CsrfToken, "content-large-create-0001")
	httpStatus(t, response, 201)
	var largeDraft draftResult
	readJSON(t, response, &largeDraft)
	largeHash := sha256.Sum256(largeDraft.Content)
	largeTx, err := lab.db.Begin(ctx)
	must(t, err)
	largeIDs := []string{largeDraft.ID}
	for i := 0; i < 99; i++ {
		id := fmt.Sprintf("tsk_%032x", i+1)
		largeIDs = append(largeIDs, id)
		_, err = largeTx.Exec(ctx, `INSERT INTO content.task_drafts(id,author_id,create_key,create_hash,content,created_at) VALUES($1,$2,$3,$4,$5,$6)`, id, charlieSession.Account.Id, fmt.Sprintf("large-page-key-%04d", i), largeHash[:], largeDraft.Content, fixedCreated)
		must(t, err)
	}
	_, err = largeTx.Exec(ctx, `UPDATE content.task_drafts SET created_at=$1 WHERE author_id=$2`, fixedCreated, charlieSession.Account.Id)
	must(t, err)
	must(t, largeTx.Commit(ctx))
	sort.Sort(sort.Reverse(sort.StringSlice(largeIDs)))

	// The first byte-bounded page must stop decoding after the first item that
	// does not fit. A malformed stored row beyond that boundary must not poison
	// an otherwise valid page; restore it before traversing the remaining pages.
	deferredID := largeIDs[len(largeIDs)-1]
	_, err = lab.db.Exec(ctx, `UPDATE content.task_drafts SET content=$1 WHERE id=$2`, json.RawMessage(`{"title":1,"entries":[]}`), deferredID)
	must(t, err)
	largePage := page("/api/me/submissions?limit=100", charlie)
	_, err = lab.db.Exec(ctx, `UPDATE content.task_drafts SET content=$1 WHERE id=$2`, largeDraft.Content, deferredID)
	must(t, err)
	if len(largePage.Items) == 0 || len(largePage.Items) >= 100 || largePage.NextCursor == nil {
		t.Fatalf("protobuf budget did not split large page: items=%d cursor=%v", len(largePage.Items), largePage.NextCursor)
	}
	var returnedLarge contentpb.TaskDraftInput
	must(t, protojson.Unmarshal(largePage.Items[0].Content, &returnedLarge))
	returnedValues := returnedLarge.Entries[0].AgentConfiguration.Parameters.Fields["nested"].GetStructValue().Fields["values"].GetListValue().Values
	if len(returnedValues) != 3000 {
		t.Fatalf("large submission was truncated: %d values", len(returnedValues))
	}
	if got := collect(url.Values{"limit": {"100"}}, charlie); !reflect.DeepEqual(got, largeIDs) {
		t.Fatalf("size-bounded pagination mismatch: got=%v want=%v", got, largeIDs)
	}
	largeActor, err := lab.clients[0].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: charlie.Value}, Audience: "content", FullMethod: contentpb.ContentService_ListMySubmissions_FullMethodName})
	must(t, err)
	largeRPC, err := clients[0].ListMySubmissions(ctx, &contentpb.ListMySubmissionsRequest{ActorAssertion: largeActor.ActorAssertion, Limit: 100})
	must(t, err)
	if proto.Size(largeRPC) > 2<<20 || len(largeRPC.Items) != len(largePage.Items) || largeRPC.NextCursor == "" {
		t.Fatalf("RPC page budget mismatch: bytes=%d rpc_items=%d http_items=%d cursor=%q", proto.Size(largeRPC), len(largeRPC.Items), len(largePage.Items), largeRPC.NextCursor)
	}
	_, err = lab.db.Exec(ctx, `DELETE FROM content.task_drafts WHERE author_id=$1`, charlieSession.Account.Id)
	must(t, err)

	// Force a real database lock wait through HTTP. A deadline is not permission
	// to blindly overwrite; after releasing the lock, confirm no revision changed.
	blocked, err := lab.db.Begin(ctx)
	must(t, err)
	defer blocked.Rollback(context.Background())
	_, err = blocked.Exec(ctx, "SELECT id FROM content.task_drafts WHERE id=$1 FOR UPDATE", draft.ID)
	must(t, err)
	check(request(0, "PUT", path, `{"expectedRevision":1,"content":`+initial+`}`, alice, aliceSession.CsrfToken, ""), 504)
	must(t, blocked.Rollback(ctx))
	var unchanged int64
	must(t, lab.db.QueryRow(ctx, "SELECT revision FROM content.task_drafts WHERE id=$1", draft.ID).Scan(&unchanged))
	if unchanged != 1 {
		t.Fatal("timed out lock waiter wrote a revision")
	}
	// The Content-only lab intentionally has no ASSET_TARGET. Gateway must fail
	// closed before issuing an unverified file-reference write.
	fileBody := `{"title":"file","summary":"","description":"","entries":[{"side":"human","title":"h","source":"s","artifacts":[{"kind":"file","assetId":"unverified"}],"permissions":{"cloudUse":false}}]}`
	check(request(0, "POST", "/api/tasks", fileBody, alice, aliceSession.CsrfToken, "content-file-rejected-01"), 503)
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
	persistedPage := page("/api/me/submissions?limit=100", alice)
	if len(persistedPage.Items) != 5 {
		t.Fatalf("restart lost list entries: %+v", persistedPage)
	}

	// mTLS identifies the caller; a valid assertion does not grant other services access.
	actor, err := lab.clients[0].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: alice.Value}, Audience: "content", FullMethod: contentpb.ContentService_GetMyTaskSubmission_FullMethodName})
	must(t, err)
	listActor, err := lab.clients[0].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: alice.Value}, Audience: "content", FullMethod: contentpb.ContentService_ListMySubmissions_FullMethodName})
	must(t, err)
	listRPC, err := clients[0].ListMySubmissions(ctx, &contentpb.ListMySubmissionsRequest{ActorAssertion: listActor.ActorAssertion, Limit: 2})
	must(t, err)
	if len(listRPC.Items) != 2 || listRPC.NextCursor == "" {
		t.Fatalf("direct list RPC mismatch: %+v", listRPC)
	}
	_, err = lab.clients[0].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: alice.Value}, Audience: "identity", FullMethod: contentpb.ContentService_ListMySubmissions_FullMethodName})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong list audience allowed: %v", err)
	}
	_, err = clients[0].ListMySubmissions(ctx, &contentpb.ListMySubmissionsRequest{ActorAssertion: actor.ActorAssertion})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("detail assertion crossed into list: %v", err)
	}
	_, err = clients[0].ListMySubmissions(ctx, &contentpb.ListMySubmissionsRequest{ActorAssertion: listActor.ActorAssertion, Kind: "entry"})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("unsupported RPC filter accepted: %v", err)
	}
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
	check(request(0, "GET", "/api/me/submissions", "", alice, "", ""), 401)
	check(request(0, "POST", "/api/tasks", initial, alice, aliceSession.CsrfToken, createKey), 401)
	_, err = clients[0].GetMyTaskSubmission(ctx, &contentpb.GetMyTaskSubmissionRequest{ActorAssertion: actor.ActorAssertion, TaskId: draft.ID})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("pre-revocation assertion survived: %v", err)
	}
	_, err = clients[0].ListMySubmissions(ctx, &contentpb.ListMySubmissionsRequest{ActorAssertion: listActor.ActorAssertion})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("pre-revocation list assertion survived: %v", err)
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
	bobListActor, err := lab.clients[1].ResolvePrincipal(ctx, &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: bob.Value}, Audience: "content", FullMethod: contentpb.ContentService_ListMySubmissions_FullMethodName})
	must(t, err)
	lab.grpc[0].Stop()
	_, err = clients[0].GetMyTaskSubmission(ctx, &contentpb.GetMyTaskSubmissionRequest{ActorAssertion: bobActor.ActorAssertion, TaskId: bobDraft.ID})
	if err == nil {
		t.Fatal("Content continued when its Identity was unavailable")
	}
	_, err = clients[0].ListMySubmissions(ctx, &contentpb.ListMySubmissionsRequest{ActorAssertion: bobListActor.ActorAssertion})
	if err == nil {
		t.Fatal("Content list continued when its Identity was unavailable")
	}
	check(request(0, "GET", "/api/me/submissions", "", bob, "", ""), 503)
	check(request(1, "GET", "/api/me/tasks/"+bobDraft.ID, "", bob, "", ""), 200)
	check(request(1, "GET", "/api/me/submissions", "", bob, "", ""), 200)
	var count int
	must(t, lab.db.QueryRow(ctx, "SELECT count(*) FROM content.task_drafts").Scan(&count))
	if count != 6 {
		t.Fatalf("denied/retried requests changed row count: %d", count)
	}
	must(t, lab.db.QueryRow(ctx, "SELECT count(*) FROM content.task_drafts WHERE state <> 'draft'").Scan(&count))
	if count != 0 {
		t.Fatal("draft automatically published")
	}
	t.Log("PASS: real HTTPS → gateway → mTLS Content process → VerifyActor → restricted PostgreSQL; private list keyset/byte pagination, method deadline, legacy-data migration, two replicas, restart and refusal paths")
}
