package postgres

import (
	"context"
	"time"
)

// 【阅读 7】Cleanup 供启动入口的后台任务分批回收过期流程、旧流程族和已过期 7 天的 web 凭据。
// 先把仍在进行的过期流程置为 expired 并抹除 PKCE 密文；删除流程族会级联删除其流程。
// 登录与鉴权读取本身检查有效期，清理延迟不会延长凭据寿命，也不删除稳定账号或 MCP 记录。
// 【阅读 7.1】每次最多处理 100 条流程并跳过已锁行，避免后台清理长时间阻塞正常登录。
func (s *Store) Cleanup(ctx context.Context) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	// Small bounded batches; expiry is enforced on reads regardless of cleanup timing.
	_, err = tx.Exec(ctx, `WITH batch AS (SELECT id FROM identity.login_flows WHERE expires_at<=clock_timestamp() AND status IN ('pending','exchanging') LIMIT 100 FOR UPDATE SKIP LOCKED) UPDATE identity.login_flows SET status='expired',verifier_cipher=NULL FROM batch WHERE login_flows.id=batch.id`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM identity.login_families WHERE id IN (SELECT f.id FROM identity.login_families f WHERE NOT EXISTS(SELECT 1 FROM identity.login_flows l WHERE l.family_id=f.id AND l.expires_at>clock_timestamp()-interval '1 day') LIMIT 100)`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM identity.credentials WHERE id IN (SELECT id FROM identity.credentials WHERE kind='web' AND expires_at<clock_timestamp()-interval '7 days' LIMIT 100)`)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
