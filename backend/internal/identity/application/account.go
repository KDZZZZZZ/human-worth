package application

import (
	"context"
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// Admin runs in the separate operator process with its restricted database role.
// It needs neither a network transport nor Google, encryption or signing keys.
type Admin struct {
	transactions Transactor
	randomToken  func() string
}

func NewAdmin(transactions Transactor, randomToken func() string) *Admin {
	return &Admin{transactions: transactions, randomToken: randomToken}
}

// 【阅读 13】ChangeAccount 仅供 backend/cmd/identity-admin/main.go 的独立运维 Job/CLI 使用，不是公开 RPC。
// 加账号行锁并检查 expected 版本后，在同一事务更新角色/状态、递增 auth_version、追加审计。
// credential/verify 比对该版本使旧会话、MCP 凭据及断言失效；恢复账号后仍需重新登录，account_id 始终不变。
func (a *Admin) ChangeAccount(ctx context.Context, actor string, r dto.ChangeAccountInput) error {
	role := domain.User
	if r.Role == "admin" {
		role = domain.Admin
	}
	if actor == "" || r.AccountID == "" || r.ExpectedVersion < 1 || (r.Role != "user" && r.Role != "admin") || !domain.ValidAccess(role, r.State) {
		return domain.ErrInvalidRequest
	}
	return a.transactions.WithinTx(ctx, func(tx TxRepos) error {
		accounts, err := tx.Accounts.LockAccounts(ctx, []string{r.AccountID})
		if errors.Is(err, ErrNotFound) {
			return Reject(NotFound, "account_not_found")
		}
		if err != nil {
			return err
		}
		account := accounts[r.AccountID]
		if err = account.ChangeAccess(r.ExpectedVersion, role, r.State); err != nil {
			return err
		}
		if err = tx.Accounts.SaveAccess(ctx, account, r.ExpectedVersion); err != nil {
			return err
		}
		return tx.Audit.Append(ctx, domain.AuditEvent{ID: "audit_" + a.randomToken(), ActorID: actor, Action: "account_change", TargetID: r.AccountID, Reason: r.Role + ":" + r.State, AuthVersion: account.Snapshot().AuthVersion})
	})
}
