package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 【阅读 1】migrations 嵌入发布时的原始 SQL；已执行文件的任何字节变化（包括注释）都会触发校验失败。
// 表结构与约束见 backend/internal/identity/repo/postgres/migrations/001_identity.sql：
//   - 【阅读 1.1】accounts 保存稳定账号、角色、状态和 auth_version；application/account.go 的 ChangeAccount 递增版本使旧凭据失效。
//   - 【阅读 1.2】external_identities 以 (issuer, subject) 唯一绑定账号；login.go 的 finishLogin 维护绑定和资料，邮箱不作关联键。
//   - 【阅读 1.3】login_families 提供同一浏览器登录替换的锁定对象；login_flows 保存一次性流程，终态必须清空 PKCE 密文。
//   - 【阅读 1.4】credentials 只保存 token_hash；web 凭据带 CSRF 密文，MCP 凭据带名称和创建请求 ID，两种形态互斥。
//   - 【阅读 1.5】audit_events 与业务写入同事务保存；delivered_at 预留投递标记，当前没有 Moderation 投递实现。
//
// 后续结构变化应新增迁移，不能修改已执行文件；这些中文说明放在 Go 中以保留历史校验和。
//
//go:embed migrations/*.sql
//go:embed migrations/*.sql
var migrations embed.FS

// 【阅读 2】Migrate 由 backend/cmd/identity/main.go 的迁移模式执行，使用独立迁移角色准备 Identity 表结构。
// 同一事务先取得模块专属咨询锁，再按文件名顺序比对 SHA-256、执行缺失迁移并登记版本；
// 任一步失败都回滚，多个副本或重试不能把半套结构标记为完成。运行服务只调用 Ready，不执行 DDL。
// 【阅读 2.1】先取得模块专属事务锁，再准备 schema 和版本表，避免多个部署进程并行改动结构。
// 【阅读 2.2】按文件名检查已执行迁移的校验和，只执行尚未登记的 SQL，并在同一事务登记版本。
// 【阅读 2.3】所有迁移与版本记录一起提交；前面任一步失败都会回滚，不留下半完成状态。
func Migrate(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7185429001)`); err != nil {
		return err
	}
	var schemaExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname='identity')`).Scan(&schemaExists); err != nil {
		return err
	}
	if !schemaExists {
		if _, err = tx.Exec(ctx, `CREATE SCHEMA identity`); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS identity.schema_migrations (version text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		sql, err := migrations.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return err
		}
		sum := sha256.Sum256(sql)
		checksum := hex.EncodeToString(sum[:])
		var previous string
		rows, err := tx.Query(ctx, `SELECT checksum FROM identity.schema_migrations WHERE version=$1`, entry.Name())
		if err != nil {
			return err
		}
		exists := rows.Next()
		if exists {
			err = rows.Scan(&previous)
		}
		rows.Close()
		if err != nil {
			return err
		}
		if err = rows.Err(); err != nil {
			return err
		}
		if exists {
			if previous != checksum {
				return errors.New("applied migration changed")
			}
			continue
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO identity.schema_migrations(version,checksum) VALUES($1,$2)`, entry.Name(), checksum); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// 【阅读 3】Ready 查询数据库及初始迁移版本，供服务就绪探针使用；完整迁移内容校验由 Migrate 负责。
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
