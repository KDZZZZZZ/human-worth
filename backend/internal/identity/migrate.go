package identity

import (
	"context"
	"embed"
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate 复用模块迁移器，保留 Identity 原有锁号与迁移校验和。
func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	return platform.Migrate(ctx, db, migrations, "identity", 7185429001)
}
func Ready(ctx context.Context, db *pgxpool.Pool) error {
	var exists bool
	err := db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity.schema_migrations WHERE version='001_identity.sql')`).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return errors.New("identity schema not ready")
	}
	return nil
}
