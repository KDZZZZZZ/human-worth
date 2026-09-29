package application

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"strings"
)

// 【阅读 1】CreateMCPToken 为当前 web 账号创建只读用途的 MCP 凭据，不能用 MCP 凭据再签发凭据。
// 通路：gateway/http.go 的 createToken → auth.go 的 verify/lockActor → 按账号和 CreateRequestId 判重
// → credentials 写 token_hash、当前 auth_version 与到期时间 → 同事务审计 → 提交后返回一次明文 token。
// 账号仍与 web 会话共用同一个 account_id；后续使用 token 经 auth.go 的 ResolvePrincipal 限制目标方法。
// 【阅读 1.1】账号行锁串行化同账号创建，数据库唯一约束兜底；即便首次响应丢失，也不会再次返回已创建的秘密。
// 重复请求返回 409，gateway/web/account.js 的 revokeLostCreation 按请求 ID 找回元数据并撤销后再让用户新建。
// 【阅读 1.2】凭据只存摘要，创建记录和审计在同一事务提交；成功后才允许向调用方返回明文。
// 【阅读 1.3】token 只存在于本次成功响应，数据库和后续列表均不能恢复它；Credential 只包含管理所需的元数据。
func (s *Service) CreateMCPToken(ctx context.Context, r dto.CreateMCPTokenInput) (dto.CreatedMCPToken, error) {
	if strings.TrimSpace(r.Name) == "" || len([]rune(r.Name)) > 80 || strings.TrimSpace(r.CreateRequestID) == "" || len(r.CreateRequestID) > 128 {
		return dto.CreatedMCPToken{}, domain.ErrInvalidRequest
	}
	actor, err := s.verifyOperation(ctx, r.ActorAssertion, domain.CreateMCPToken)
	if err != nil {
		return dto.CreatedMCPToken{}, err
	}
	var result dto.CreatedMCPToken
	err = s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		if _, err := lockActor(ctx, tx, actor.principal); err != nil {
			return err
		}
		exists, err := tx.Credentials.CreatedRequestExists(ctx, actor.principal.AccountID, r.CreateRequestID)
		if err != nil {
			return err
		}
		if exists {
			return Reject(AlreadyExists, "idempotency_conflict")
		}
		token, id := s.secrets.RandomToken(), s.newID("cred")
		record, err := tx.Credentials.InsertCredential(ctx, NewCredential{ID: id, AccountID: actor.principal.AccountID, Kind: domain.MCPRead, Hash: s.secrets.Digest(token), AuthVersion: actor.principal.AuthVersion, Name: strings.TrimSpace(r.Name), CreateRequestID: r.CreateRequestID, TTL: s.config.McpTTL})
		if err != nil {
			return err
		}
		if err = s.audit(ctx, tx, actor.principal.AccountID, "mcp_created", id, "", actor.principal.AuthVersion); err != nil {
			return err
		}
		data := record.Credential.Snapshot()
		result = dto.CreatedMCPToken{Token: token, Credential: dto.MCPToken{ID: id, Name: record.Name, CreateRequestID: record.CreateRequestID, CreatedAt: data.CreatedAt, ExpiresAt: data.ExpiresAt, State: "active"}}
		return nil
	})
	if err != nil {
		return dto.CreatedMCPToken{}, err
	}
	return result, nil
}

// 【阅读 2】ListMyMCPTokens 验证当前 web 会话后，按凭据 ID 游标分页读取本人 MCP 元数据，不查询或返回原始 token。
// 状态先判断撤销或账号版本失配，再判断过期；版本失配也显示 revoked，但不伪造实际 revoked_at。
// gateway/http.go 的 tokenJSON 映射状态和时间，gateway/web/account.js 的 listTokens 展示并提供撤销入口。
// 【阅读 2.1】多取一条仅用于判断下一页；游标指向本页最后保留的 ID，后续查询用 id > cursor，避免边界重复。
func (s *Service) ListMyMCPTokens(ctx context.Context, r dto.ListMCPTokensInput) (dto.MCPTokenPage, error) {
	actor, err := s.verifyOperation(ctx, r.ActorAssertion, domain.ListMCPTokens)
	if err != nil {
		return dto.MCPTokenPage{}, err
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return dto.MCPTokenPage{}, domain.ErrInvalidRequest
	}
	cursor := ""
	if r.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(r.Cursor)
		if err != nil || len(decoded) > 100 || !strings.HasPrefix(string(decoded), "cred_") {
			return dto.MCPTokenPage{}, Reject(Invalid, "invalid_cursor")
		}
		cursor = string(decoded)
	}
	items, err := s.reader.ListMCP(ctx, MCPQuery{AccountID: actor.principal.AccountID, Cursor: cursor, Limit: int(limit) + 1, AuthVersion: actor.principal.AuthVersion})
	if err != nil {
		return dto.MCPTokenPage{}, err
	}
	result := dto.MCPTokenPage{Items: items}
	if len(items) > int(limit) {
		result.Items = items[:limit]
		result.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(result.Items[len(result.Items)-1].ID))
	}
	return result, nil
}

// 【阅读 3】RevokeMCPToken 经 web 身份与事务锁校验后，只撤销属于该账号的 MCP 凭据，其他账号的 ID 统一返回不存在。
// 首次撤销将 revoked_at 与审计一起提交，重复撤销成功返回且不重复审计；网关 revokeToken 映射 HTTP 204。
// auth.go 的 credential 每次读取当前撤销状态，因此旧 token 和未到期的 ActorAssertion 都不能继续通过校验。
func (s *Service) RevokeMCPToken(ctx context.Context, r dto.RevokeMCPTokenInput) error {
	if !strings.HasPrefix(r.CredentialID, "cred_") || len(r.CredentialID) > 100 {
		return domain.ErrInvalidRequest
	}
	actor, err := s.verifyOperation(ctx, r.ActorAssertion, domain.RevokeMCPToken)
	if err != nil {
		return err
	}
	return s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		if _, err := lockActor(ctx, tx, actor.principal); err != nil {
			return err
		}
		record, err := tx.Credentials.LockCredential(ctx, r.CredentialID, actor.principal.AccountID, domain.MCPRead)
		if errors.Is(err, ErrNotFound) {
			return Reject(NotFound, "credential_not_found")
		}
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		if !record.Credential.Revoke(now) {
			return nil
		}
		if err = tx.Credentials.SaveRevocation(ctx, record.Credential); err != nil {
			return err
		}
		return s.audit(ctx, tx, actor.principal.AccountID, "mcp_revoked", r.CredentialID, "", actor.principal.AuthVersion)
	})
}
