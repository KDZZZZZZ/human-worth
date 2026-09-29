package domain

import (
	"errors"
	"testing"
	"time"
)

func TestCredentialAccountVersionAndTime(t *testing.T) {
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	account, err := NewAccount("acc_one", "用户")
	if err != nil {
		t.Fatal(err)
	}
	credential, err := RestoreCredential(CredentialData{ID: "cred_one", AccountID: "acc_one", Kind: WebSession, AuthVersion: 1, ExpiresAt: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err = credential.Validate(account, WebSession, now); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{now.Add(time.Second), now.Add(time.Hour)} {
		if !errors.Is(credential.Validate(account, WebSession, at), ErrUnauthenticated) {
			t.Fatal("expired credential accepted")
		}
	}
	if !errors.Is(credential.Validate(account, MCPRead, now), ErrUnauthenticated) {
		t.Fatal("wrong purpose accepted")
	}
	if err = account.ChangeAccess(1, Admin, "active"); err != nil {
		t.Fatal(err)
	}
	if account.Snapshot().ID != "acc_one" || account.Snapshot().AuthVersion != 2 {
		t.Fatal("unstable account/version")
	}
	if !errors.Is(credential.Validate(account, WebSession, now), ErrUnauthenticated) || credential.State(2, now) != "revoked" {
		t.Fatal("old version remained valid")
	}
	if !errors.Is(account.ChangeAccess(1, User, "active"), ErrRevisionConflict) {
		t.Fatal("stale update accepted")
	}
	if err = account.ChangeAccess(2, Admin, "active"); err != nil {
		t.Fatal(err)
	}
	if account.Snapshot().AuthVersion != 3 {
		t.Fatal("same-value change failed to invalidate credentials")
	}
	if !credential.Revoke(now) || credential.Revoke(now.Add(time.Second)) || !credential.Snapshot().RevokedAt.Equal(now) {
		t.Fatal("revoke must be permanent and idempotent")
	}
	if credential.State(1, now.Add(time.Hour)) != "revoked" {
		t.Fatal("revocation precedence changed")
	}
	copy := account.Snapshot()
	copy.State = "disabled"
	if account.RequireActive() != nil {
		t.Fatal("snapshot mutated account")
	}
	if err = account.ChangeAccess(3, User, "disabled"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(account.RequireActive(), ErrAccountDisabled) {
		t.Fatal("disabled account accepted")
	}
}

func TestLoginFlowTransitions(t *testing.T) {
	now := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	newFlow := func() LoginFlow {
		f, err := RestoreFlow(FlowData{ID: "flow_1", FamilyID: "browser_1", Status: Pending, ExpiresAt: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	flow := newFlow()
	if err := flow.Claim("attempt-1", now); err != nil {
		t.Fatal(err)
	}
	if flow.Claim("attempt-2", now) == nil || flow.Complete("attempt-2", now) == nil || flow.Fail("attempt-2") {
		t.Fatal("another attempt replaced owner")
	}
	if flow.Complete("attempt-1", now.Add(time.Minute)) == nil {
		t.Fatal("expiry boundary accepted")
	}
	if err := flow.Complete("attempt-1", now); err != nil {
		t.Fatal(err)
	}
	if flow.Cancel() || flow.Fail("attempt-1") || flow.Claim("new", now) == nil {
		t.Fatal("terminal success changed")
	}
	for _, status := range []FlowStatus{Succeeded, Failed, Cancelled, Expired} {
		f, err := RestoreFlow(FlowData{ID: "flow_1", FamilyID: "browser_1", Status: status, ExpiresAt: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		if f.Cancel() || f.Fail("attempt") || f.Claim("attempt", now) == nil || f.Complete("attempt", now) == nil {
			t.Fatalf("terminal %s reopened", status)
		}
	}
	for _, denied := range []bool{false, true} {
		f := newFlow()
		if err := f.RejectProvider(denied, now); err != nil {
			t.Fatal(err)
		}
		want := Failed
		if denied {
			want = Cancelled
		}
		if f.Snapshot().Status != want {
			t.Fatal("wrong provider outcome")
		}
	}
	cancelled := newFlow()
	if err := cancelled.Claim("a", now); err != nil {
		t.Fatal(err)
	}
	if !cancelled.Cancel() || cancelled.Complete("a", now) == nil {
		t.Fatal("cancelled flow completed")
	}
	expired := newFlow()
	if expired.Claim("a", now.Add(time.Minute)) == nil {
		t.Fatal("expired flow claimed")
	}
}

func TestOperationPolicy(t *testing.T) {
	anonymous := Principal{ClientKind: Anonymous}
	web := Principal{AccountID: "acc", CredentialID: "cred", AuthVersion: 1, ClientKind: WebSession, Role: User}
	mcp := web
	mcp.ClientKind = MCPRead
	mcp.Role = Admin
	admin := web
	admin.Role = Admin
	for _, tc := range []struct {
		op      Operation
		actor   Principal
		allowed bool
	}{
		{ReadTask, anonymous, true}, {ReadOwnSubmission, anonymous, false},
		{CreateDraft, web, true}, {CreateDraft, mcp, false}, {ReadOwnSubmission, mcp, false},
		{ReadTask, mcp, true}, {ViewStatistics, mcp, true}, {CastVote, mcp, false},
		{StartRun, web, false}, {StartRun, admin, true}, {StartRun, mcp, false},
		{CreateMCPToken, mcp, false}, {CurrentSession, web, true},
	} {
		policy, ok := PolicyFor(tc.op)
		if !ok {
			t.Fatal(tc.op)
		}
		if (policy.Authorize(tc.actor) == nil) != tc.allowed {
			t.Fatalf("wrong permission op=%s kind=%d role=%d", tc.op, tc.actor.ClientKind, tc.actor.Role)
		}
	}
	if _, ok := PolicyFor("unknown"); ok {
		t.Fatal("unknown operation allowed")
	}
	anonymous.AccountID = "injected"
	policy, _ := PolicyFor(ReadTask)
	if policy.Authorize(anonymous) == nil {
		t.Fatal("malformed anonymous accepted")
	}
}
