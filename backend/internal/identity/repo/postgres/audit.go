package postgres

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

func (t *transaction) Append(ctx context.Context, e domain.AuditEvent) error {
	_, err := t.tx.Exec(ctx, `INSERT INTO identity.audit_events(id,actor_id,action,target_id,reason,auth_version) VALUES($1,$2,$3,$4,$5,$6)`, e.ID, e.ActorID, e.Action, e.TargetID, e.Reason, e.AuthVersion)
	return persistence(err)
}
