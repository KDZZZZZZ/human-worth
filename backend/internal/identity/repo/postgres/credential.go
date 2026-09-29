package postgres

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"time"
)

func (s *Store) ReadCredential(ctx context.Context, key application.CredentialKey) (application.AuthSnapshot, error) {
	var result application.AuthSnapshot
	var a domain.AccountData
	var c domain.CredentialData
	var dbKind, dbRole string
	var revoked *time.Time
	query := `SELECT c.id,c.account_id,c.kind,c.auth_version,c.created_at,c.expires_at,c.revoked_at,COALESCE(c.csrf_cipher,''),a.id,a.display_name,a.role,a.state,a.auth_version,clock_timestamp() FROM identity.credentials c JOIN identity.accounts a ON a.id=c.account_id WHERE `
	var arg any = key.Hash
	if key.ID != "" {
		query += `c.id=$1`
		arg = key.ID
	} else {
		query += `c.token_hash=$1`
	}
	err := s.db.QueryRow(ctx, query, arg).Scan(&c.ID, &c.AccountID, &dbKind, &c.AuthVersion, &c.CreatedAt, &c.ExpiresAt, &revoked, &result.CSRF, &a.ID, &a.DisplayName, &dbRole, &a.State, &a.AuthVersion, &result.DBNow)
	if err != nil {
		return result, persistence(err)
	}
	c.Kind = kind(dbKind)
	a.Role = role(dbRole)
	if revoked != nil {
		c.RevokedAt = *revoked
	}
	result.Account, err = domain.RestoreAccount(a)
	if err != nil {
		return result, application.Reject(application.Unavailable, "identity_unavailable")
	}
	result.Credential, err = domain.RestoreCredential(c)
	if err != nil {
		return result, application.Reject(application.Unavailable, "identity_unavailable")
	}
	return result, nil
}
func (t *transaction) FindWebAccount(ctx context.Context, hash []byte) (string, error) {
	var id string
	err := t.tx.QueryRow(ctx, `SELECT account_id FROM identity.credentials WHERE token_hash=$1 AND kind='web'`, hash).Scan(&id)
	return id, persistence(err)
}
func (t *transaction) LockCredential(ctx context.Context, id, owner string, k domain.ClientKind) (application.CredentialRecord, error) {
	return scanCredential(t.tx.QueryRow(ctx, `SELECT `+credentialColumns+` FROM identity.credentials WHERE id=$1 AND account_id=$2 AND kind=$3 FOR UPDATE`, id, owner, kindName(k)))
}
func (t *transaction) CreatedRequestExists(ctx context.Context, owner, request string) (bool, error) {
	var exists bool
	err := t.tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM identity.credentials WHERE account_id=$1 AND create_request_id=$2)`, owner, request).Scan(&exists)
	return exists, persistence(err)
}
func (t *transaction) InsertCredential(ctx context.Context, c application.NewCredential) (application.CredentialRecord, error) {
	var csrf, name, request any
	if c.Kind == domain.WebSession {
		csrf = c.CSRF
	} else {
		name = c.Name
		request = c.CreateRequestID
	}
	return scanCredential(t.tx.QueryRow(ctx, `INSERT INTO identity.credentials(id,account_id,kind,token_hash,auth_version,csrf_cipher,name,create_request_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,clock_timestamp()+$9*interval '1 second') RETURNING `+credentialColumns, c.ID, c.AccountID, kindName(c.Kind), c.Hash, c.AuthVersion, csrf, name, request, int64(c.TTL/time.Second)))
}
func (t *transaction) SaveRevocation(ctx context.Context, c domain.Credential) error {
	d := c.Snapshot()
	_, err := t.tx.Exec(ctx, `UPDATE identity.credentials SET revoked_at=COALESCE(revoked_at,$3) WHERE id=$1 AND account_id=$2`, d.ID, d.AccountID, d.RevokedAt)
	return persistence(err)
}
func (t *transaction) RevokeWebHash(ctx context.Context, hash []byte) error {
	_, err := t.tx.Exec(ctx, `UPDATE identity.credentials SET revoked_at=COALESCE(revoked_at,clock_timestamp()) WHERE token_hash=$1 AND kind='web'`, hash)
	return persistence(err)
}
func (s *Store) ListMCP(ctx context.Context, q application.MCPQuery) ([]dto.MCPToken, error) {
	rows, err := s.db.Query(ctx, `SELECT `+credentialColumns+`,clock_timestamp() FROM identity.credentials WHERE account_id=$1 AND kind='mcp' AND id>$2 ORDER BY id LIMIT $3`, q.AccountID, q.Cursor, q.Limit)
	if err != nil {
		return nil, persistence(err)
	}
	defer rows.Close()
	var result []dto.MCPToken
	for rows.Next() {
		var c domain.CredentialData
		var item dto.MCPToken
		var dbKind, csrf string
		var now time.Time
		if err = rows.Scan(&c.ID, &c.AccountID, &dbKind, &c.AuthVersion, &c.CreatedAt, &c.ExpiresAt, &item.RevokedAt, &csrf, &item.Name, &item.CreateRequestID, &now); err != nil {
			return nil, persistence(err)
		}
		c.Kind = kind(dbKind)
		if item.RevokedAt != nil {
			c.RevokedAt = *item.RevokedAt
		}
		cred, e := domain.RestoreCredential(c)
		if e != nil {
			return nil, application.Reject(application.Unavailable, "identity_unavailable")
		}
		item.ID, item.CreatedAt, item.ExpiresAt, item.State = c.ID, c.CreatedAt, c.ExpiresAt, cred.State(q.AuthVersion, now)
		result = append(result, item)
	}
	return result, persistence(rows.Err())
}
