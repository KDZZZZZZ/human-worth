package postgres

import (
	"context"
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type Store struct{ db *pgxpool.Pool }

func New(db *pgxpool.Pool) *Store { return &Store{db: db} }

type transaction struct{ tx pgx.Tx }

func (s *Store) WithinTx(ctx context.Context, run func(application.TxRepos) error) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return persistence(err)
	}
	// Request cancellation cannot prevent bounded rollback/connection cleanup, including on panic.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	scoped := &transaction{tx: tx}
	if err = run(application.TxRepos{Accounts: scoped, LoginFlows: scoped, Credentials: scoped, Audit: scoped, Now: scoped.now}); err != nil {
		return err
	}
	return persistence(tx.Commit(ctx))
}
func (t *transaction) now(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := t.tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, persistence(err)
}
func persistence(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "credentials_account_id_create_request_id_key" {
		return application.Reject(application.AlreadyExists, "idempotency_conflict")
	}
	return application.Reject(application.Unavailable, "identity_unavailable")
}
