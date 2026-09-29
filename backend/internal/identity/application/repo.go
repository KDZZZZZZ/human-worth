package application

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"time"
)

type CredentialKey struct {
	ID   string
	Hash []byte
}
type AuthSnapshot struct {
	Account    domain.Account
	Credential domain.Credential
	CSRF       string
	DBNow      time.Time
}
type CredentialRecord struct {
	Credential                  domain.Credential
	CSRF, Name, CreateRequestID string
}
type NewCredential struct {
	ID, AccountID               string
	Kind                        domain.ClientKind
	Hash                        []byte
	AuthVersion                 int64
	CSRF, Name, CreateRequestID string
	TTL                         time.Duration
}
type FlowRecord struct {
	Flow                                 domain.LoginFlow
	StateHash, NonceHash                 []byte
	Verifier, ConfigVersion, RedirectURI string
}
type NewFlow struct {
	ID, FamilyID                         string
	CookieHash, StateHash, NonceHash     []byte
	Verifier, ConfigVersion, RedirectURI string
}
type FlowRef struct{ ID, FamilyID string }
type MCPQuery struct {
	AccountID, Cursor string
	Limit             int
	AuthVersion       int64
}

type IdentityReader interface {
	ReadCredential(context.Context, CredentialKey) (AuthSnapshot, error)
	ListMCP(context.Context, MCPQuery) ([]dto.MCPToken, error)
}
type Transactor interface {
	WithinTx(context.Context, func(TxRepos) error) error
}
type TxRepos struct {
	Accounts    AccountRepository
	LoginFlows  LoginRepository
	Credentials CredentialRepository
	Audit       AuditRepository
	Now         func(context.Context) (time.Time, error)
}
type AccountRepository interface {
	LockExternalIdentity(context.Context, domain.ExternalKey) error
	FindExternalAccount(context.Context, domain.ExternalKey) (string, error)
	InsertAccount(context.Context, domain.Account) error
	InsertExternalIdentity(context.Context, domain.ExternalKey, string) error
	LockAccounts(context.Context, []string) (map[string]domain.Account, error)
	UpdateProfile(context.Context, string, domain.ExternalKey, GoogleIdentity) error
	SaveAccess(context.Context, domain.Account, int64) error
}
type LoginRepository interface {
	FindFlow(context.Context, []byte) (FlowRef, error)
	CreateFamily(context.Context, domain.LoginFamily) error
	LockFamily(context.Context, string) error
	CancelPending(context.Context, string) error
	InsertFlow(context.Context, NewFlow) error
	LockFlow(context.Context, string) (FlowRecord, error)
	SaveFlow(context.Context, FlowRecord, domain.FlowData) error
}
type CredentialRepository interface {
	FindWebAccount(context.Context, []byte) (string, error)
	LockCredential(context.Context, string, string, domain.ClientKind) (CredentialRecord, error)
	CreatedRequestExists(context.Context, string, string) (bool, error)
	InsertCredential(context.Context, NewCredential) (CredentialRecord, error)
	SaveRevocation(context.Context, domain.Credential) error
	RevokeWebHash(context.Context, []byte) error
}
type AuditRepository interface {
	Append(context.Context, domain.AuditEvent) error
}
type MaintenanceRepository interface{ Cleanup(context.Context) error }
