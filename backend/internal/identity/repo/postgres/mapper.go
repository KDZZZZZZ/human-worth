package postgres

import (
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"github.com/jackc/pgx/v5"
	"time"
)

// 【阅读 7】role 将数据库角色映射为领域枚举；未知值会在恢复领域对象时拒绝。
func role(raw string) domain.Role {
	switch raw {
	case "user":
		return domain.User
	case "admin":
		return domain.Admin
	default:
		return domain.RoleUnspecified
	}
}
func roleName(value domain.Role) string {
	if value == domain.Admin {
		return "admin"
	}
	return "user"
}
func kind(raw string) domain.ClientKind {
	switch raw {
	case "web":
		return domain.WebSession
	case "mcp":
		return domain.MCPRead
	default:
		return domain.KindUnspecified
	}
}
func kindName(value domain.ClientKind) string {
	if value == domain.MCPRead {
		return "mcp"
	}
	return "web"
}
func scanAccount(row pgx.Row) (domain.Account, error) {
	var data domain.AccountData
	var dbRole string
	if err := row.Scan(&data.ID, &data.DisplayName, &dbRole, &data.State, &data.AuthVersion); err != nil {
		return domain.Account{}, persistence(err)
	}
	data.Role = role(dbRole)
	account, err := domain.RestoreAccount(data)
	if err != nil {
		return domain.Account{}, application.Reject(application.Unavailable, "identity_unavailable")
	}
	return account, nil
}

const credentialColumns = `id,account_id,kind,auth_version,created_at,expires_at,revoked_at,COALESCE(csrf_cipher,''),COALESCE(name,''),COALESCE(create_request_id,'')`

func scanCredential(row pgx.Row) (application.CredentialRecord, error) {
	var result application.CredentialRecord
	var data domain.CredentialData
	var dbKind string
	var revoked *time.Time
	if err := row.Scan(&data.ID, &data.AccountID, &dbKind, &data.AuthVersion, &data.CreatedAt, &data.ExpiresAt, &revoked, &result.CSRF, &result.Name, &result.CreateRequestID); err != nil {
		return result, persistence(err)
	}
	data.Kind = kind(dbKind)
	if revoked != nil {
		data.RevokedAt = *revoked
	}
	var err error
	result.Credential, err = domain.RestoreCredential(data)
	if err != nil {
		return result, application.Reject(application.Unavailable, "identity_unavailable")
	}
	return result, nil
}
