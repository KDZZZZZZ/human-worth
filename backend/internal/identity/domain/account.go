package domain

import "unicode/utf8"

type AccountData struct {
	ID, DisplayName, State string
	Role                   Role
	AuthVersion            int64
}
type Account struct{ data AccountData }

func RestoreAccount(data AccountData) (Account, error) {
	if data.ID == "" || utf8.RuneCountInString(data.DisplayName) < 1 || utf8.RuneCountInString(data.DisplayName) > 100 || !ValidAccess(data.Role, data.State) || data.AuthVersion < 1 {
		return Account{}, ErrInvalidRequest
	}
	return Account{data: data}, nil
}
func NewAccount(id, name string) (Account, error) {
	return RestoreAccount(AccountData{ID: id, DisplayName: name, State: "active", Role: User, AuthVersion: 1})
}
func (a Account) Snapshot() AccountData { return a.data }
func ValidAccess(role Role, state string) bool {
	return (role == User || role == Admin) && (state == "active" || state == "disabled")
}
func (a Account) RequireActive() error {
	if a.data.State != "active" {
		return ErrAccountDisabled
	}
	return nil
}
func (a *Account) ChangeAccess(expected int64, role Role, state string) error {
	if expected < 1 || !ValidAccess(role, state) {
		return ErrInvalidRequest
	}
	if a.data.AuthVersion != expected {
		return ErrRevisionConflict
	}
	a.data.Role, a.data.State = role, state
	a.data.AuthVersion++
	return nil
}

// ExternalKey is the stable provider identity; email is never part of this key.
type ExternalKey struct{ Issuer, Subject string }
