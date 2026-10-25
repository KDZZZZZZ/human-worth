package voting

import (
	"context"
	"embed"
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	return platform.Migrate(ctx, db, migrations, "voting", 7185429011)
}
func Ready(ctx context.Context, db *pgxpool.Pool) error {
	var ok bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM voting.schema_migrations WHERE version='002_backend_completion.sql')`).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return errors.New("voting schema not ready")
	}
	return nil
}
