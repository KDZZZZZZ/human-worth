package domain

type Role int32

const (
	RoleUnspecified Role = iota
	User
	Admin
)

type ClientKind int32

const (
	KindUnspecified ClientKind = iota
	Anonymous
	WebSession
	MCPRead
)

type Principal struct {
	AccountID    string
	Role         Role
	ClientKind   ClientKind
	CredentialID string
	AuthVersion  int64
}

func (p Principal) Validate() error {
	if p.ClientKind == Anonymous {
		if p.AccountID != "" || p.CredentialID != "" || p.AuthVersion != 0 || p.Role != RoleUnspecified {
			return ErrPurposeForbidden
		}
		return nil
	}
	if (p.ClientKind != WebSession && p.ClientKind != MCPRead) || p.AccountID == "" || p.CredentialID == "" || p.AuthVersion < 1 || (p.Role != User && p.Role != Admin) {
		return ErrInvalidActor
	}
	return nil
}
