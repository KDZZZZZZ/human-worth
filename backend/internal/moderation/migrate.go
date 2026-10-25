package moderation

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
	return platform.Migrate(ctx, db, migrations, "moderation", 7185429006)
}
func Ready(ctx context.Context, db *pgxpool.Pool) error {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM moderation.schema_migrations WHERE version='004_reports_actions.sql')`).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("moderation schema not ready")
	}
	return nil
}
