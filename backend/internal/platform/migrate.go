package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrate 在模块 schema 内串行执行带校验和的迁移；已执行的文件不能被悄悄改写。
func Migrate(ctx context.Context, db *pgxpool.Pool, files fs.FS, schema string, lock int64) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", lock); err != nil {
		return err
	}
	var exists bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1)", schema).Scan(&exists); err != nil {
		return err
	}
	name := pgx.Identifier{schema}.Sanitize()
	if !exists {
		if _, err = tx.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
			return err
		}
	}
	table := name + ".schema_migrations"
	if _, err = tx.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+table+" (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())"); err != nil {
		return err
	}
	entries, err := fs.ReadDir(files, "migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		body, err := fs.ReadFile(files, "migrations/"+entry.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(body)
		checksum := hex.EncodeToString(sum[:])
		var previous string
		err = tx.QueryRow(ctx, "SELECT checksum FROM "+table+" WHERE version=$1", entry.Name()).Scan(&previous)
		if err == nil {
			if previous != checksum {
				return errors.New("applied migration changed")
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if _, err = tx.Exec(ctx, string(body)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO "+table+"(version, checksum) VALUES($1,$2)", entry.Name(), checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
