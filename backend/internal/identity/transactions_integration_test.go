//go:build integration

package identity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/repo/postgres"
	identitygrpc "github.com/KDZZZZZZ/human-worth/backend/internal/identity/transport/grpc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/codes"
)

func TestOperatorCLIWithRestrictedRole(t *testing.T) {
	l := newIdentityLab(t)
	session, me := l.login(t, "operator-account", nil)
	roleName := "operator_" + strings.ToLower(strings.ReplaceAll(randomToken()[:12], "-", "x"))
	roleID := pgx.Identifier{roleName}.Sanitize()
	password := randomToken()
	_, err := l.db.Exec(t.Context(), "CREATE ROLE "+roleID+" LOGIN PASSWORD '"+password+"'; GRANT USAGE ON SCHEMA identity TO "+roleID+"; GRANT SELECT, UPDATE(role,state,auth_version,updated_at) ON identity.accounts TO "+roleID+"; GRANT INSERT ON identity.audit_events TO "+roleID)
	must(t, err)
	t.Cleanup(func() { _, _ = l.db.Exec(context.Background(), "DROP OWNED BY "+roleID+"; DROP ROLE "+roleID) })
	cfg := l.db.Config().Copy()
	cfg.ConnConfig.User, cfg.ConnConfig.Password = roleName, password
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	t.Cleanup(pool.Close)
	for _, sql := range []string{`SELECT * FROM identity.credentials`, `UPDATE identity.accounts SET display_name='forbidden'`, `DELETE FROM identity.audit_events`} {
		if _, err = pool.Exec(t.Context(), sql); err == nil {
			t.Fatal("operator exceeded database capability")
		}
	}
	dir := t.TempDir()
	dsnFile := filepath.Join(dir, "database-url")
	binary := filepath.Join(dir, "identity-admin")
	// ConnString retains the originally parsed URL, not changes made to Config fields.
	dsn := (&url.URL{Scheme: "postgres", User: url.UserPassword(roleName, password), Host: net.JoinHostPort(cfg.ConnConfig.Host, strconv.Itoa(int(cfg.ConnConfig.Port))), Path: cfg.ConnConfig.Database, RawQuery: "sslmode=disable"}).String()
	must(t, os.WriteFile(dsnFile, []byte(dsn), 0600))
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/identity-admin")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build operator: %v %s", err, output)
	}
	run := func() (string, error) {
		cmd := exec.CommandContext(t.Context(), binary, "--account", me.Account.Id, "--role", "admin", "--state", "active", "--expected-version", "1")
		cmd.Env = append(os.Environ(), "IDENTITY_DATABASE_URL_FILE="+dsnFile, "OPERATOR_IDENTITY=integration-operator")
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	output, err := run()
	if err != nil {
		t.Fatalf("restricted operator failed: %v %s", err, output)
	}
	output, err = run()
	if err == nil || !strings.Contains(output, "revision_conflict") {
		t.Fatal("CLI failed to reject stale version")
	}
	var changed bool
	must(t, l.db.QueryRow(t.Context(), `SELECT role='admin' AND auth_version=2 FROM identity.accounts WHERE id=$1`, me.Account.Id).Scan(&changed))
	if !changed {
		t.Fatal("operator update missing")
	}
	httpStatus(t, l.request(t, 1, "GET", "/api/me", []*http.Cookie{session}, "", nil), 401)
}

func flowRequest(t *testing.T, l *identityLab, subject string) *pb.CompleteGoogleLoginRequest {
	t.Helper()
	flow, err := l.clients[0].StartGoogleLogin(t.Context(), &pb.StartGoogleLoginRequest{})
	must(t, err)
	u, err := url.Parse(flow.AuthorizationUrl)
	must(t, err)
	code := l.provider.issue(t, flow.AuthorizationUrl, subject, nil)
	return &pb.CompleteGoogleLoginRequest{FlowCookie: flow.FlowCookie, State: u.Query().Get("state"), Outcome: &pb.CompleteGoogleLoginRequest_Code{Code: code}}
}

func writeAssertion(t *testing.T, l *identityLab, session *http.Cookie, csrf, method string) string {
	t.Helper()
	actor, err := l.clients[0].ResolvePrincipal(t.Context(), &pb.ResolvePrincipalRequest{Credential: &pb.ResolvePrincipalRequest_SessionCookie{SessionCookie: session.Value}, Audience: "identity", FullMethod: method, Origin: l.origin, CsrfToken: csrf})
	must(t, err)
	return actor.ActorAssertion
}

func TestRejectedLoginOutcomeCommits(t *testing.T) {
	l := newIdentityLab(t)
	for _, outcome := range []string{"access_denied", "server_error"} {
		t.Run(outcome, func(t *testing.T) {
			req := flowRequest(t, l, "unused")
			req.Outcome = &pb.CompleteGoogleLoginRequest_ProviderError{ProviderError: outcome}
			before := l.provider.exchanges.Load()
			result, err := l.clients[0].CompleteGoogleLogin(t.Context(), req)
			state := "failed"
			if outcome == "access_denied" {
				state = "cancelled"
				must(t, err)
				if result.RedirectPath != "/?login=cancelled" || result.SessionCookie != "" {
					t.Fatal("wrong cancellation response")
				}
			} else {
				grpcCode(t, err, codes.Unauthenticated)
			}
			var stored string
			var cleared bool
			must(t, l.db.QueryRow(t.Context(), `SELECT status,verifier_cipher IS NULL FROM identity.login_flows WHERE cookie_hash=$1`, digest(req.FlowCookie)).Scan(&stored, &cleared))
			if stored != state || !cleared {
				t.Fatal("rejected outcome rolled back or retained verifier")
			}
			_, err = l.clients[1].CompleteGoogleLogin(t.Context(), req)
			grpcCode(t, err, codes.InvalidArgument)
			if l.provider.exchanges.Load() != before {
				t.Fatal("rejected callback exchanged a code")
			}
		})
	}
}

func TestAuditFailureRollsBackUseCases(t *testing.T) {
	l := newIdentityLab(t)
	session, me := l.login(t, "atomic-account", nil)
	createActor := func() string {
		return writeAssertion(t, l, session, me.CsrfToken, pb.IdentityService_CreateMcpToken_FullMethodName)
	}
	created, err := l.clients[0].CreateMcpToken(t.Context(), &pb.CreateMcpTokenRequest{ActorAssertion: createActor(), Name: "existing", CreateRequestId: "existing"})
	must(t, err)
	_, err = l.db.Exec(t.Context(), `CREATE FUNCTION identity.reject_test_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected audit failure'; END $$; CREATE TRIGGER reject_test_audit BEFORE INSERT ON identity.audit_events FOR EACH ROW EXECUTE FUNCTION identity.reject_test_audit()`)
	must(t, err)
	var accountsBefore, credentialsBefore, auditBefore int
	counts := func() (int, int, int) {
		t.Helper()
		var a, c, e int
		must(t, l.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM identity.accounts),(SELECT count(*) FROM identity.credentials),(SELECT count(*) FROM identity.audit_events)`).Scan(&a, &c, &e))
		return a, c, e
	}
	accountsBefore, credentialsBefore, auditBefore = counts()
	t.Run("new login and old session replacement", func(t *testing.T) {
		req := flowRequest(t, l, "must-not-persist")
		req.PreviousSessionCookie = session.Value
		_, err := l.clients[1].CompleteGoogleLogin(t.Context(), req)
		grpcCode(t, err, codes.Unavailable)
		var failed bool
		var linked int
		must(t, l.db.QueryRow(t.Context(), `SELECT status='failed' AND verifier_cipher IS NULL FROM identity.login_flows WHERE cookie_hash=$1`, digest(req.FlowCookie)).Scan(&failed))
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.external_identities WHERE subject='must-not-persist'`).Scan(&linked))
		if !failed || linked != 0 {
			t.Fatal("failed login left an account or reusable flow")
		}
	})
	t.Run("MCP creation", func(t *testing.T) {
		_, err := l.clients[1].CreateMcpToken(t.Context(), &pb.CreateMcpTokenRequest{ActorAssertion: createActor(), Name: "rollback", CreateRequestId: "rollback"})
		grpcCode(t, err, codes.Unavailable)
		var count int
		must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.credentials WHERE create_request_id='rollback'`).Scan(&count))
		if count != 0 {
			t.Fatal("credential persisted without audit")
		}
	})
	t.Run("MCP revocation", func(t *testing.T) {
		_, err := l.clients[1].RevokeMcpToken(t.Context(), &pb.RevokeMcpTokenRequest{ActorAssertion: writeAssertion(t, l, session, me.CsrfToken, pb.IdentityService_RevokeMcpToken_FullMethodName), CredentialId: created.Credential.Id})
		grpcCode(t, err, codes.Unavailable)
		var active bool
		must(t, l.db.QueryRow(t.Context(), `SELECT revoked_at IS NULL FROM identity.credentials WHERE id=$1`, created.Credential.Id).Scan(&active))
		if !active {
			t.Fatal("revocation persisted without audit")
		}
	})
	t.Run("logout", func(t *testing.T) {
		_, err := l.clients[1].LogoutCurrentSession(t.Context(), &pb.LogoutCurrentSessionRequest{ActorAssertion: writeAssertion(t, l, session, me.CsrfToken, pb.IdentityService_LogoutCurrentSession_FullMethodName)})
		grpcCode(t, err, codes.Unavailable)
	})
	t.Run("account change", func(t *testing.T) {
		admin := application.NewAdmin(postgres.New(l.db), randomToken)
		err := admin.ChangeAccount(t.Context(), "operator", dto.ChangeAccountInput{AccountID: me.Account.Id, Role: "admin", State: "disabled", ExpectedVersion: 1})
		grpcCode(t, identitygrpc.Error(err), codes.Unavailable)
		var unchanged bool
		must(t, l.db.QueryRow(t.Context(), `SELECT role='user' AND state='active' AND auth_version=1 FROM identity.accounts WHERE id=$1`, me.Account.Id).Scan(&unchanged))
		if !unchanged {
			t.Fatal("account changed without audit")
		}
	})
	if a, c, e := counts(); a != accountsBefore || c != credentialsBefore || e != auditBefore {
		t.Fatal("failed use case left partial writes")
	}
	httpStatus(t, l.request(t, 0, "GET", "/api/me", []*http.Cookie{session}, "", nil), 200)
}

func TestTransactionCleanupAndSharedScope(t *testing.T) {
	l := newIdentityLab(t)
	cfg := l.runtimeDB.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	t.Cleanup(pool.Close)
	store := postgres.New(pool)
	for _, mode := range []string{"error", "panic", "cancel", "commit"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			id := newID("acc")
			account, err := domain.NewAccount(id, "transaction test")
			must(t, err)
			failure := errors.New("abort callback")
			var recovered any
			func() {
				defer func() { recovered = recover() }()
				err = store.WithinTx(ctx, func(tx application.TxRepos) error {
					if err := tx.Accounts.InsertAccount(ctx, account); err != nil {
						return err
					}
					if _, err := tx.Credentials.InsertCredential(ctx, application.NewCredential{ID: newID("cred"), AccountID: id, Kind: domain.WebSession, Hash: digest(randomToken()), AuthVersion: 1, CSRF: "test", TTL: time.Hour}); err != nil {
						return err
					}
					if err := tx.Audit.Append(ctx, domain.AuditEvent{ID: newID("audit"), ActorID: id, Action: "test", TargetID: id}); err != nil {
						return err
					}
					switch mode {
					case "error":
						return failure
					case "panic":
						panic(failure)
					case "cancel":
						cancel()
					}
					return nil
				})
			}()
			if mode == "panic" && recovered != failure {
				t.Fatal("panic lost")
			}
			if mode == "error" && !errors.Is(err, failure) {
				t.Fatal("callback error lost")
			}
			if mode == "cancel" && err == nil {
				t.Fatal("cancelled commit accepted")
			}
			if mode == "commit" {
				must(t, err)
			}
			check, stop := context.WithTimeout(t.Context(), 2*time.Second)
			defer stop()
			// MaxConns=1 makes a leaked transaction observable instead of opening another.
			must(t, pool.Ping(check))
			var a, c, e int
			must(t, l.db.QueryRow(check, `SELECT (SELECT count(*) FROM identity.accounts WHERE id=$1),(SELECT count(*) FROM identity.credentials WHERE account_id=$1),(SELECT count(*) FROM identity.audit_events WHERE actor_id=$1)`, id).Scan(&a, &c, &e))
			want := 0
			if mode == "commit" {
				want = 1
			}
			if a != want || c != want || e != want {
				t.Fatal("repositories did not share transaction outcome")
			}
		})
	}
}

func TestMCPWriteRechecksAfterLockWait(t *testing.T) {
	l := newIdentityLab(t)
	for _, mode := range []string{"revoked", "account changed", "expired"} {
		t.Run(mode, func(t *testing.T) {
			session, me := l.login(t, "wait-"+mode, nil)
			if mode == "expired" {
				_, err := l.db.Exec(t.Context(), `UPDATE identity.credentials SET expires_at=clock_timestamp()+interval '3 seconds' WHERE token_hash=$1`, digest(session.Value))
				must(t, err)
			}
			assertion := writeAssertion(t, l, session, me.CsrfToken, pb.IdentityService_CreateMcpToken_FullMethodName)
			tx, err := l.db.Begin(t.Context())
			must(t, err)
			defer tx.Rollback(context.Background())
			var credential string
			must(t, tx.QueryRow(t.Context(), `SELECT id FROM identity.credentials WHERE token_hash=$1 FOR UPDATE`, digest(session.Value)).Scan(&credential))
			if mode == "account changed" {
				// Account is acquired before credential by the writer; lock it before dispatch.
				_, err = tx.Exec(t.Context(), `SELECT id FROM identity.accounts WHERE id=$1 FOR UPDATE`, me.Account.Id)
				must(t, err)
			}
			done := make(chan error, 1)
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			go func() {
				_, err := l.clients[1].CreateMcpToken(ctx, &pb.CreateMcpTokenRequest{ActorAssertion: assertion, Name: "late", CreateRequestId: "late-" + mode})
				done <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				must(t, l.db.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND usename=$1 AND wait_event_type='Lock' AND query LIKE '%FOR UPDATE%')`, l.runtimeDB.Config().ConnConfig.User).Scan(&waiting))
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("writer never reached lock wait")
				}
				time.Sleep(10 * time.Millisecond)
			}
			switch mode {
			case "revoked":
				_, err = tx.Exec(t.Context(), `UPDATE identity.credentials SET revoked_at=clock_timestamp() WHERE id=$1`, credential)
			case "account changed":
				_, err = tx.Exec(t.Context(), `UPDATE identity.accounts SET auth_version=auth_version+1 WHERE id=$1`, me.Account.Id)
			case "expired":
				_, err = tx.Exec(t.Context(), `SELECT pg_sleep(GREATEST(EXTRACT(EPOCH FROM expires_at-clock_timestamp()),0)+0.1) FROM identity.credentials WHERE id=$1`, credential)
			}
			must(t, err)
			must(t, tx.Commit(t.Context()))
			grpcCode(t, <-done, codes.Unauthenticated)
			var count int
			must(t, l.db.QueryRow(t.Context(), `SELECT count(*) FROM identity.credentials WHERE account_id=$1 AND kind='mcp'`, me.Account.Id).Scan(&count))
			if count != 0 {
				t.Fatal("invalid actor created credential after waiting")
			}
		})
	}
}
