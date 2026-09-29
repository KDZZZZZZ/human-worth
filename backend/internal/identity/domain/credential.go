package domain

import "time"

type CredentialData struct {
	ID, AccountID                   string
	Kind                            ClientKind
	AuthVersion                     int64
	CreatedAt, ExpiresAt, RevokedAt time.Time
}
type Credential struct{ data CredentialData }

func RestoreCredential(data CredentialData) (Credential, error) {
	if data.ID == "" || data.AccountID == "" || (data.Kind != WebSession && data.Kind != MCPRead) || data.AuthVersion < 1 || data.ExpiresAt.IsZero() {
		return Credential{}, ErrInvalidRequest
	}
	return Credential{data: data}, nil
}
func (c Credential) Snapshot() CredentialData { return c.data }
func (c Credential) Validate(a Account, expected ClientKind, now time.Time) error {
	account := a.Snapshot()
	if account.ID != c.data.AccountID || account.State != "active" || account.AuthVersion != c.data.AuthVersion || c.data.Kind != expected || !c.data.RevokedAt.IsZero() || !now.Before(c.data.ExpiresAt) {
		return ErrUnauthenticated
	}
	return nil
}
func (c Credential) Principal(a Account) Principal {
	return Principal{AccountID: c.data.AccountID, CredentialID: c.data.ID, ClientKind: c.data.Kind, AuthVersion: c.data.AuthVersion, Role: a.Snapshot().Role}
}
func (c *Credential) Revoke(now time.Time) bool {
	if !c.data.RevokedAt.IsZero() {
		return false
	}
	c.data.RevokedAt = now
	return true
}
func (c Credential) State(version int64, now time.Time) string {
	if !c.data.RevokedAt.IsZero() || c.data.AuthVersion != version {
		return "revoked"
	}
	if !now.Before(c.data.ExpiresAt) {
		return "expired"
	}
	return "active"
}
