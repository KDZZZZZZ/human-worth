package application

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// 【阅读 2】StartGoogleLogin 处理 gateway/http.go 的 start 转发，建立有效期 10 分钟的一次性登录流程。
// 完整通路：start → 本函数写 login_families/login_flows → adapter/google/oauth.go 的 AuthorizationURL → 浏览器前往 Google
// → 网关 callback → CompleteGoogleLogin → claim → adapter/google/oauth.go 的 Exchange → finishLogin → 网关设置会话 Cookie。
// cookie/state/nonce 落库只存 SHA-256；PKCE verifier 经 adapter/security/crypto.go 的 Seal 加密，授权 URL 只带 challenge，换码时再解密使用。
// 【阅读 2.1】旧 Cookie 找回的是流程族而非一个可复用登录结果；同族锁让跨副本的并发“重新登录”顺序生效。
// 【阅读 2.2】替换会取消 pending 和正在换码的 exchanging 流程；迟到结果还须经 finishLogin 的再次状态检查。
// 【阅读 2.3】先提交可被任意副本读取的流程，再返回 Cookie 和重定向 URL，避免浏览器拿到尚未落库的流程。
func (s *Service) StartGoogleLogin(ctx context.Context, r dto.StartLoginInput) (dto.StartLoginResult, error) {
	if s.google == nil {
		return dto.StartLoginResult{}, Reject(Unavailable, "login_unavailable")
	}
	if len(r.PreviousFlowCookie) > 512 {
		return dto.StartLoginResult{}, domain.ErrInvalidRequest
	}
	id, cookie, state, nonce, verifier := s.newID("flow"), s.secrets.RandomToken(), s.secrets.RandomToken(), s.secrets.RandomToken(), s.secrets.RandomToken()
	encrypted, err := s.secrets.Seal(verifier, id+":verifier")
	if err != nil {
		return dto.StartLoginResult{}, err
	}
	settings := s.google.Settings()
	err = s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		family := ""
		if r.PreviousFlowCookie != "" {
			previous, err := tx.LoginFlows.FindFlow(ctx, s.secrets.Digest(r.PreviousFlowCookie))
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			family = previous.FamilyID
		}
		if family == "" {
			family = s.newID("browser")
			if err := tx.LoginFlows.CreateFamily(ctx, domain.LoginFamily{ID: family}); err != nil {
				return err
			}
		} else if err := tx.LoginFlows.LockFamily(ctx, family); err != nil {
			return err
		}
		if err := tx.LoginFlows.CancelPending(ctx, family); err != nil {
			return err
		}
		return tx.LoginFlows.InsertFlow(ctx, NewFlow{ID: id, FamilyID: family, CookieHash: s.secrets.Digest(cookie), StateHash: s.secrets.Digest(state), NonceHash: s.secrets.Digest(nonce), Verifier: encrypted, ConfigVersion: settings.Version, RedirectURI: settings.RedirectURI})
	})
	if err != nil {
		return dto.StartLoginResult{}, err
	}
	return dto.StartLoginResult{AuthorizationURL: s.google.AuthorizationURL(state, nonce, verifier), FlowCookie: cookie, MaxAgeSeconds: 600}, nil
}

// 【阅读 1】claimedFlow 是 claim 提交成功后交给外部换码步骤的临时数据，不是可重新使用的登录凭据。
// ID/Family 定位流程及锁，Attempt 绑定这一次换码；Verifier 为解密后的 PKCE 值，Nonce 实际保存 nonce 的摘要。
// finishLogin 必须再次核对数据库中的 Attempt 和状态，不能仅因持有该对象就创建会话。
type claimedFlow struct {
	ID, FamilyID, Attempt, Verifier string
	Nonce                           []byte
}

// 【阅读 4】claim 按 Cookie 摘要找流程，依次锁定流程族和流程行，检查 pending、有效期、state、配置版本及回调地址。
// 用户拒绝授权时提交 cancelled（其他提供方错误为 failed）并清空密文；布尔返回值表示用户取消。
// 成功时解密 verifier，将 pending 改为 exchanging 并写入唯一 Attempt，提交后才允许调用外部换码接口。
// 所以两个副本收到相同回调也只有一个能进入 adapter/google/oauth.go 的 Exchange。
func (s *Service) claim(ctx context.Context, r dto.CompleteLoginInput) (claimedFlow, bool, error) {
	var claimed claimedFlow
	var outcome error
	cancelled := false
	err := s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		ref, err := tx.LoginFlows.FindFlow(ctx, s.secrets.Digest(r.FlowCookie))
		if errors.Is(err, ErrNotFound) {
			return domain.ErrInvalidFlow
		}
		if err != nil {
			return err
		}
		if err = tx.LoginFlows.LockFamily(ctx, ref.FamilyID); err != nil {
			return err
		}
		record, err := tx.LoginFlows.LockFlow(ctx, ref.ID)
		if err != nil {
			return err
		}
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		if err = record.Flow.CanClaim(now); err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(record.StateHash, s.secrets.Digest(r.State)) != 1 {
			return domain.ErrInvalidFlow
		}
		settings := s.google.Settings()
		if record.ConfigVersion != settings.Version || record.RedirectURI != settings.RedirectURI {
			return Reject(Unavailable, "login_unavailable")
		}
		previous := record.Flow.Snapshot()
		if r.ProviderError != nil {
			cancelled = *r.ProviderError == "access_denied"
			if err = record.Flow.RejectProvider(cancelled, now); err != nil {
				return err
			}
			if !cancelled {
				outcome = Reject(Unauthenticated, "invalid_google_login")
			}
			// A rejected login is a committed business outcome, not a transaction failure.
			return tx.LoginFlows.SaveFlow(ctx, record, previous)
		}
		verifier, err := s.secrets.Open(record.Verifier, ref.ID+":verifier")
		if err != nil {
			return err
		}
		attempt := s.secrets.RandomToken()
		if err = record.Flow.Claim(attempt, now); err != nil {
			return err
		}
		if err = tx.LoginFlows.SaveFlow(ctx, record, previous); err != nil {
			return err
		}
		claimed = claimedFlow{ID: ref.ID, FamilyID: ref.FamilyID, Attempt: attempt, Verifier: verifier, Nonce: record.NonceHash}
		return nil
	})
	if err != nil {
		return claimedFlow{}, false, err
	}
	return claimed, cancelled, outcome
}

// 【阅读 3】CompleteGoogleLogin 接收网关 callback 的 state 与 code/error，串联领取流程、验证 Google 身份和本地会话提交。
// claim 的事务已结束后才执行 adapter/google/oauth.go 的 Exchange，避免外部 HTTP 等待长期占用数据库行锁。
// 取消只返回取消跳转；成功由 finishLogin 写账号、凭据、流程终态和审计，最后由网关回写浏览器 Cookie。
// 【阅读 3.1】外部授权码不能重放；提交响应丢失时流程可能已经 succeeded，故仅清理仍匹配本次 Attempt 的 exchanging。
// 即便原请求取消，也给清理独立的 2 秒期限；清理失败仍由 server.go 的 Cleanup 最终过期回收。
func (s *Service) CompleteGoogleLogin(ctx context.Context, r dto.CompleteLoginInput) (dto.CompleteLoginResult, error) {
	if s.google == nil {
		return dto.CompleteLoginResult{}, Reject(Unavailable, "login_unavailable")
	}
	if r.FlowCookie == "" || len(r.FlowCookie) > 512 || r.State == "" || len(r.State) > 1024 || len(r.PreviousSessionCookie) > 512 {
		return dto.CompleteLoginResult{}, domain.ErrInvalidRequest
	}
	if (r.Code == nil) == (r.ProviderError == nil) {
		return dto.CompleteLoginResult{}, domain.ErrInvalidRequest
	}
	if r.Code != nil && (len(*r.Code) == 0 || len(*r.Code) > 4096) {
		return dto.CompleteLoginResult{}, domain.ErrInvalidRequest
	}
	if r.ProviderError != nil && (len(*r.ProviderError) == 0 || len(*r.ProviderError) > 128) {
		return dto.CompleteLoginResult{}, domain.ErrInvalidRequest
	}
	flow, cancelled, err := s.claim(ctx, r)
	if err != nil {
		return dto.CompleteLoginResult{}, err
	}
	if cancelled {
		return dto.CompleteLoginResult{RedirectPath: "/?login=cancelled"}, nil
	}
	// Both database transactions end before/after this external call, never around it.
	profile, err := s.google.Exchange(ctx, *r.Code, flow.Verifier, flow.Nonce)
	if err == nil {
		var result dto.CompleteLoginResult
		result, err = s.finishLogin(ctx, flow, profile, r.PreviousSessionCookie)
		if err == nil {
			return result, nil
		}
	}
	// Never replay a code. A lost commit response may already have succeeded.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = s.failLogin(cleanup, flow)
	return dto.CompleteLoginResult{}, err
}

func (s *Service) failLogin(ctx context.Context, flow claimedFlow) error {
	return s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		if err := tx.LoginFlows.LockFamily(ctx, flow.FamilyID); err != nil {
			return err
		}
		record, err := tx.LoginFlows.LockFlow(ctx, flow.ID)
		if err != nil {
			return err
		}
		previous := record.Flow.Snapshot()
		if !record.Flow.Fail(flow.Attempt) {
			return nil
		}
		return tx.LoginFlows.SaveFlow(ctx, record, previous)
	})
}

// 【阅读 5】finishLogin 将 adapter/google/oauth.go 的 Exchange 已验证资料绑定到稳定账号，并原子完成会话替换。
// 数据通路：再次确认流程仍有效 → 按 (issuer, subject) 查找或创建账号 → 读取当前账号版本
// → 更新显示资料 → 撤销旧 web 凭据 → 插入新凭据摘要与 CSRF 密文 → 流程 succeeded、清 PKCE、写审计 → 提交。
// 提交后返回的原始 token 只交给 gateway/http.go 的 callback 设置 Cookie；以后由 auth.go 的 credential 按摘要识别。
// 【阅读 5.1】Google 返回期间可能发生重新登录、取消或过期；在同族锁下重查，拒绝任何迟到结果继续落库。
// 【阅读 5.2】以提供方和 subject 的事务咨询锁串行化首次绑定；并发首次登录只生成一个稳定账号，不重试 Google 授权码。
// 邮箱仅更新为资料，不能把相同邮箱的不同 subject 合并为同一账号。
// 【阅读 5.3】切换账号时可能同时涉及新旧账号；统一按 ID 顺序加锁，避免两个相反方向的切换互相死锁。
// 与 auth.go 的 lockActor、ChangeAccount 共用账号锁，随后读取的状态和版本才能用于签发新会话。
// 【阅读 5.4】旧会话撤销与新会话创建同事务提交；任何后续写入失败都会回滚，原会话不会单独丢失。
func (s *Service) finishLogin(ctx context.Context, flow claimedFlow, profile GoogleIdentity, oldCookie string) (dto.CompleteLoginResult, error) {
	token, id, csrf := s.secrets.RandomToken(), s.newID("cred"), s.secrets.RandomToken()
	encrypted, err := s.secrets.Seal(csrf, id+":csrf")
	if err != nil {
		return dto.CompleteLoginResult{}, err
	}
	err = s.transactions.WithinTx(ctx, func(tx TxRepos) error {
		if err := tx.LoginFlows.LockFamily(ctx, flow.FamilyID); err != nil {
			return err
		}
		record, err := tx.LoginFlows.LockFlow(ctx, flow.ID)
		if err != nil {
			return err
		}
		previous := record.Flow.Snapshot()
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		if err = record.Flow.CanComplete(flow.Attempt, now); err != nil {
			return err
		}
		key := domain.ExternalKey{Issuer: s.google.Settings().Issuer, Subject: profile.Subject}
		// Serialize first-login association across replicas without retrying the code.
		if err = tx.Accounts.LockExternalIdentity(ctx, key); err != nil {
			return err
		}
		accountID, err := tx.Accounts.FindExternalAccount(ctx, key)
		if errors.Is(err, ErrNotFound) {
			accountID = s.newID("acc")
			account, newErr := domain.NewAccount(accountID, profile.Name)
			if newErr != nil {
				return newErr
			}
			if err = tx.Accounts.InsertAccount(ctx, account); err != nil {
				return err
			}
			err = tx.Accounts.InsertExternalIdentity(ctx, key, accountID)
		}
		if err != nil {
			return err
		}
		ids := []string{accountID}
		if oldCookie != "" {
			oldAccount, err := tx.Credentials.FindWebAccount(ctx, s.secrets.Digest(oldCookie))
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if oldAccount != "" {
				ids = append(ids, oldAccount)
			}
		}
		// The repository acquires all account locks in sorted order.
		accounts, err := tx.Accounts.LockAccounts(ctx, ids)
		if err != nil {
			return err
		}
		account := accounts[accountID]
		if err = account.RequireActive(); err != nil {
			return err
		}
		if err = tx.Accounts.UpdateProfile(ctx, accountID, key, profile); err != nil {
			return err
		}
		if err = tx.Credentials.RevokeWebHash(ctx, s.secrets.Digest(oldCookie)); err != nil {
			return err
		}
		version := account.Snapshot().AuthVersion
		if _, err = tx.Credentials.InsertCredential(ctx, NewCredential{ID: id, AccountID: accountID, Kind: domain.WebSession, Hash: s.secrets.Digest(token), AuthVersion: version, CSRF: encrypted, TTL: s.config.SessionTTL}); err != nil {
			return err
		}
		// Account/credential lock waits can outlive the flow; roll all writes back then.
		now, err = tx.Now(ctx)
		if err != nil {
			return err
		}
		if err = record.Flow.Complete(flow.Attempt, now); err != nil {
			return err
		}
		if err = tx.LoginFlows.SaveFlow(ctx, record, previous); err != nil {
			return err
		}
		return s.audit(ctx, tx, accountID, "login", id, "google", version)
	})
	if err != nil {
		return dto.CompleteLoginResult{}, err
	}
	return dto.CompleteLoginResult{RedirectPath: "/", SessionCookie: token, MaxAgeSeconds: int32(s.config.SessionTTL / time.Second)}, nil
}
