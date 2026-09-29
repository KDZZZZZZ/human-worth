package postgres

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

func (t *transaction) FindFlow(ctx context.Context, hash []byte) (application.FlowRef, error) {
	var f application.FlowRef
	err := t.tx.QueryRow(ctx, `SELECT id,family_id FROM identity.login_flows WHERE cookie_hash=$1`, hash).Scan(&f.ID, &f.FamilyID)
	return f, persistence(err)
}
func (t *transaction) CreateFamily(ctx context.Context, f domain.LoginFamily) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO identity.login_families(id) VALUES($1)`, f.ID)
	return persistence(err)
}
func (t *transaction) LockFamily(ctx context.Context, id string) error {
	var found string
	return persistence(t.tx.QueryRow(ctx, `SELECT id FROM identity.login_families WHERE id=$1 FOR UPDATE`, id).Scan(&found))
}
func (t *transaction) CancelPending(ctx context.Context, id string) error {
	_, err := t.tx.Exec(ctx, `UPDATE identity.login_flows SET status='cancelled',verifier_cipher=NULL WHERE family_id=$1 AND status IN ('pending','exchanging')`, id)
	return persistence(err)
}
func (t *transaction) InsertFlow(ctx context.Context, f application.NewFlow) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO identity.login_flows(id,family_id,cookie_hash,state_hash,nonce_hash,verifier_cipher,config_version,redirect_uri,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'pending',clock_timestamp()+interval '10 minutes')`, f.ID, f.FamilyID, f.CookieHash, f.StateHash, f.NonceHash, f.Verifier, f.ConfigVersion, f.RedirectURI)
	return persistence(err)
}
func (t *transaction) LockFlow(ctx context.Context, id string) (application.FlowRecord, error) {
	var f application.FlowRecord
	var data domain.FlowData
	err := t.tx.QueryRow(ctx, `SELECT id,family_id,status,COALESCE(attempt_id,''),expires_at,state_hash,nonce_hash,COALESCE(verifier_cipher,''),config_version,redirect_uri FROM identity.login_flows WHERE id=$1 FOR UPDATE`, id).Scan(&data.ID, &data.FamilyID, &data.Status, &data.Attempt, &data.ExpiresAt, &f.StateHash, &f.NonceHash, &f.Verifier, &f.ConfigVersion, &f.RedirectURI)
	if err != nil {
		return f, persistence(err)
	}
	f.Flow, err = domain.RestoreFlow(data)
	if err != nil {
		return f, application.Reject(application.Unavailable, "identity_unavailable")
	}
	return f, nil
}
func (t *transaction) SaveFlow(ctx context.Context, f application.FlowRecord, previous domain.FlowData) error {
	d := f.Flow.Snapshot()
	var verifier any = f.Verifier
	if d.Status != domain.Pending && d.Status != domain.Exchanging {
		verifier = nil
	}
	var attempt any
	if d.Attempt != "" {
		attempt = d.Attempt
	}
	result, err := t.tx.Exec(ctx, `UPDATE identity.login_flows SET status=$2,attempt_id=$3,verifier_cipher=$4 WHERE id=$1 AND status=$5 AND COALESCE(attempt_id,'')=$6`, d.ID, d.Status, attempt, verifier, previous.Status, previous.Attempt)
	if err != nil {
		return persistence(err)
	}
	if result.RowsAffected() != 1 {
		return domain.ErrInvalidFlow
	}
	return nil
}
