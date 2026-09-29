package application

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// 【阅读 4】verifiedActor 保存联合认证快照和已验证主体，不直接序列化给 HTTP。
// principal 只含可信账号/凭据标识及当前授权状态；snapshot 保留账号资料和 CSRF 密文。
type verifiedActor struct {
	principal domain.Principal
	snapshot  AuthSnapshot
}

func principalDTO(p domain.Principal) dto.Principal {
	return dto.Principal{AccountID: p.AccountID, CredentialID: p.CredentialID, ClientKind: int32(p.ClientKind), Role: int32(p.Role), AuthVersion: p.AuthVersion}
}

// 【阅读 6】credential 从 Identity 自有库恢复当前身份：入口凭据按 Digest(raw) 查询，内部断言按凭据 ID 查询。
// login.go 的 finishLogin 和 mcp.go 的 CreateMCPToken 写入这些记录；这里联合校验种类、
// 撤销/过期、账号启用状态及签发时 auth_version，失败不会降级为匿名或使用缓存身份。
func (s *Service) credential(ctx context.Context, key CredentialKey, kind domain.ClientKind) (verifiedActor, error) {
	row, err := s.reader.ReadCredential(ctx, key)
	if errors.Is(err, ErrNotFound) {
		return verifiedActor{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return verifiedActor{}, err
	}
	if err = row.Credential.Validate(row.Account, kind, row.DBNow); err != nil {
		return verifiedActor{}, err
	}
	return verifiedActor{principal: row.Credential.Principal(row.Account), snapshot: row}, nil
}

func (s *Service) target(audience, method string) (Target, domain.Policy, error) {
	target, ok := s.targets[method]
	if !ok || target.Audience != audience {
		return Target{}, domain.Policy{}, domain.ErrPurposeForbidden
	}
	policy, _ := domain.PolicyFor(target.Operation)
	return target, policy, nil
}

// 【阅读 5】ResolvePrincipal 接收 gateway/http.go 的 actorFor 提交的单一凭据，签发绑定目标方法的 ActorAssertion。
// 顺序为用途检查 → credential 恢复账号 → MCP/管理员限制 → web 写操作的 Origin 与 CSRF 校验 → 签名。
// 服务调用权限由 transport/grpc/authorization.go 的 Authorization 提前检查；业务端仍须调用 VerifyActor，不能仅信任此响应中的账号 ID。
// 【阅读 5.1】CSRF 明文由 GetCurrentSession 返回当前浏览器；从库中按同一凭据用途解密后做常量时间比较。
// 【阅读 5.2】校验全部完成后才签发，签名密钥与 adapter/security/crypto.go 中保护 PKCE/CSRF 的加密密钥环分开配置。
func (s *Service) ResolvePrincipal(ctx context.Context, r dto.ResolvePrincipalInput) (dto.ResolvePrincipalResult, error) {
	target, policy, err := s.target(r.Audience, r.FullMethod)
	if err != nil {
		return dto.ResolvePrincipalResult{}, err
	}
	actor := verifiedActor{principal: domain.Principal{ClientKind: domain.Anonymous}}
	if r.SessionCookie != nil && r.MCPToken != nil {
		return dto.ResolvePrincipalResult{}, domain.ErrInvalidRequest
	}
	raw, kind := r.SessionCookie, domain.WebSession
	if r.MCPToken != nil {
		raw, kind = r.MCPToken, domain.MCPRead
	}
	if raw != nil {
		if len(*raw) == 0 || len(*raw) > 512 {
			return dto.ResolvePrincipalResult{}, domain.ErrUnauthenticated
		}
		actor, err = s.credential(ctx, CredentialKey{Hash: s.secrets.Digest(*raw)}, kind)
		if err != nil {
			return dto.ResolvePrincipalResult{}, err
		}
	}
	if err = policy.Authorize(actor.principal); err != nil {
		return dto.ResolvePrincipalResult{}, err
	}
	if policy.Write && actor.principal.ClientKind == domain.WebSession {
		if r.Origin != s.config.Origin || r.CSRFToken == "" || len(r.CSRFToken) > 512 {
			return dto.ResolvePrincipalResult{}, Reject(Forbidden, "csrf_invalid")
		}
		csrf, err := s.secrets.Open(actor.snapshot.CSRF, actor.principal.CredentialID+":csrf")
		if err != nil {
			return dto.ResolvePrincipalResult{}, err
		}
		if subtle.ConstantTimeCompare([]byte(csrf), []byte(r.CSRFToken)) != 1 {
			return dto.ResolvePrincipalResult{}, Reject(Forbidden, "csrf_invalid")
		}
	}
	signed, err := s.tokens.Sign(actor.principal, target)
	if err != nil {
		return dto.ResolvePrincipalResult{}, err
	}
	return dto.ResolvePrincipalResult{Principal: principalDTO(actor.principal), ActorAssertion: signed}, nil
}

// 【阅读 9】verify 验证 ActorAssertion 的长度、签名算法、kid、签发者、受众、方法和有效期，再恢复当前身份。
// 非匿名断言必须回查 credential；退出、撤销、停用或账号版本变更后，尚未过期的 JWT 也会失效。
// 数据库不可用时返回错误；这里只做本次校验，不为业务服务建立跨库事务或缓存授权结果。
// 【阅读 9.1】签名证明由 Identity 签发，回查证明凭据当前仍有效，两道检查缺一不可。
func (s *Service) verify(ctx context.Context, raw, audience, method string) (verifiedActor, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return verifiedActor{}, domain.ErrInvalidActor
	}
	target, policy, err := s.target(audience, method)
	if err != nil {
		return verifiedActor{}, err
	}
	claimed, err := s.tokens.Verify(raw, target)
	if err != nil {
		return verifiedActor{}, err
	}
	if claimed.ClientKind == domain.Anonymous {
		if !policy.Anonymous {
			return verifiedActor{}, domain.ErrPurposeForbidden
		}
		return verifiedActor{principal: claimed}, nil
	}
	// A signed assertion never replaces checking current account/credential state.
	actor, err := s.credential(ctx, CredentialKey{ID: claimed.CredentialID}, claimed.ClientKind)
	if err != nil {
		return verifiedActor{}, err
	}
	if actor.principal != claimed {
		return verifiedActor{}, domain.ErrInvalidActor
	}
	if err = policy.Authorize(actor.principal); err != nil {
		return verifiedActor{}, err
	}
	return actor, nil
}

func (s *Service) verifyOperation(ctx context.Context, raw string, op domain.Operation) (verifiedActor, error) {
	target := s.operations[op]
	return s.verify(ctx, raw, target.Audience, target.Method)
}

// caller is supplied by the authenticated transport, never by request DTO fields.
func (s *Service) VerifyActor(ctx context.Context, caller string, r dto.VerifyActorInput) (dto.PrincipalResult, error) {
	actor, err := s.verify(ctx, r.ActorAssertion, caller, r.FullMethod)
	if err != nil {
		return dto.PrincipalResult{}, err
	}
	return dto.PrincipalResult{Principal: principalDTO(actor.principal)}, nil
}

// 【阅读 12】lockActor 在 Identity 写事务内按账号 → web 凭据的固定顺序加行锁并重新检查当前有效性。
// LogoutCurrentSession 与 mcp.go 的创建/撤销在 verify 后调用它，避免校验与写入之间账号被停用或会话被撤销。
// 账号锁也与 ChangeAccount 串行化，并使同账号并发创建 MCP 凭据能够可靠判重。
func lockActor(ctx context.Context, tx TxRepos, principal domain.Principal) (domain.Credential, error) {
	accounts, err := tx.Accounts.LockAccounts(ctx, []string{principal.AccountID})
	if errors.Is(err, ErrNotFound) {
		return domain.Credential{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Credential{}, err
	}
	record, err := tx.Credentials.LockCredential(ctx, principal.CredentialID, principal.AccountID, domain.WebSession)
	if errors.Is(err, ErrNotFound) {
		return domain.Credential{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.Credential{}, err
	}
	// Read database time after every lock wait, not from the statement's old snapshot.
	now, err := tx.Now(ctx)
	if err != nil {
		return domain.Credential{}, err
	}
	account := accounts[principal.AccountID]
	if err = record.Credential.Validate(account, domain.WebSession, now); err != nil {
		return domain.Credential{}, err
	}
	if record.Credential.Principal(account) != principal {
		return domain.Credential{}, domain.ErrUnauthenticated
	}
	return record.Credential, nil
}
