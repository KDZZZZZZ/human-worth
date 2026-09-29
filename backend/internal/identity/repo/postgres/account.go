package postgres

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"sort"
)

func (t *transaction) LockExternalIdentity(ctx context.Context, key domain.ExternalKey) error {
	_, err := t.tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key.Issuer+":"+key.Subject)
	return persistence(err)
}
func (t *transaction) FindExternalAccount(ctx context.Context, key domain.ExternalKey) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx, `SELECT account_id FROM identity.external_identities WHERE issuer=$1 AND subject=$2`, key.Issuer, key.Subject).Scan(&id)
	return id, persistence(err)
}
func (t *transaction) InsertAccount(ctx context.Context, account domain.Account) error {
	a := account.Snapshot()
	_, err := t.tx.Exec(ctx, `INSERT INTO identity.accounts(id,display_name) VALUES($1,$2)`, a.ID, a.DisplayName)
	return persistence(err)
}
func (t *transaction) InsertExternalIdentity(ctx context.Context, key domain.ExternalKey, id string) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO identity.external_identities(issuer,subject,account_id) VALUES($1,$2,$3)`, key.Issuer, key.Subject, id)
	return persistence(err)
}
func (t *transaction) LockAccounts(ctx context.Context, ids []string) (map[string]domain.Account, error) {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	result := make(map[string]domain.Account, len(ids))
	for _, id := range ids {
		if _, ok := result[id]; ok {
			continue
		}
		a, err := scanAccount(t.tx.QueryRow(ctx, `SELECT id,display_name,role,state,auth_version FROM identity.accounts WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return nil, err
		}
		result[id] = a
	}
	return result, nil
}
func (t *transaction) UpdateProfile(ctx context.Context, id string, key domain.ExternalKey, p application.GoogleIdentity) error {
	if _, err := t.tx.Exec(ctx, `UPDATE identity.accounts SET display_name=$2,updated_at=clock_timestamp() WHERE id=$1`, id, p.Name); err != nil {
		return persistence(err)
	}
	_, err := t.tx.Exec(ctx, `UPDATE identity.external_identities SET email=$3,email_verified=$4,updated_at=clock_timestamp() WHERE issuer=$1 AND subject=$2`, key.Issuer, key.Subject, p.Email, p.EmailVerified)
	return persistence(err)
}
func (t *transaction) SaveAccess(ctx context.Context, a domain.Account, expected int64) error {
	d := a.Snapshot()
	result, err := t.tx.Exec(ctx, `UPDATE identity.accounts SET role=$2,state=$3,auth_version=$4,updated_at=clock_timestamp() WHERE id=$1 AND auth_version=$5`, d.ID, roleName(d.Role), d.State, d.AuthVersion, expected)
	if err != nil {
		return persistence(err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrRevisionConflict
	}
	return nil
}
