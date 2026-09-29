package application

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// 【阅读 10】GetCurrentSession 验证 web 会话断言并解密该凭据的 CSRF，返回稳定账号、显示名和当前角色。
// gateway/http.go 的 me 映射为 /api/me；gateway/web/account.js 将 CSRF 放入写请求头，原始会话仍保留在 HttpOnly Cookie。
func (s *Service) GetCurrentSession(ctx context.Context, r dto.CurrentSessionInput) (dto.CurrentSessionResult, error) {
	actor, err := s.verifyOperation(ctx, r.ActorAssertion, domain.CurrentSession)
	if err != nil {
		return dto.CurrentSessionResult{}, err
	}
	csrf, err := s.secrets.Open(actor.snapshot.CSRF, actor.principal.CredentialID+":csrf")
	if err != nil {
		return dto.CurrentSessionResult{}, err
	}
	return dto.CurrentSessionResult{AccountID: actor.principal.AccountID, DisplayName: actor.snapshot.Account.Snapshot().DisplayName, Role: int32(actor.principal.Role), CSRFToken: csrf}, nil
}

// 【阅读 11】LogoutCurrentSession 经 verify 与事务内 lockActor 后，原子写入当前 web 凭据的撤销时间和退出审计。
// 提交成功才通知 gateway/http.go 的 logout 清 Cookie；后续 ResolvePrincipal/VerifyActor 均会拒绝该凭据。
// 退出不删除账号、不改变其他 MCP 凭据，也不重置任何以稳定账号为键的业务资格。
func (s *Service) LogoutCurrentSession(ctx context.Context, r dto.LogoutInput) error {
	actor, err := s.verifyOperation(ctx, r.ActorAssertion, domain.Logout)
	if err != nil {
		return err
	}
	return s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		credential, err := lockActor(ctx, tx, actor.principal)
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		credential.Revoke(now)
		if err = tx.Credentials.SaveRevocation(ctx, credential); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.principal.AccountID, "logout", actor.principal.CredentialID, "", actor.principal.AuthVersion)
	})
}
