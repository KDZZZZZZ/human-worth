package application

import (
	"context"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// 【阅读 3】GoogleIdentity 只承载通过 ID token 验证并规范化后的 Google 资料，不是本地账号对象。
// login.go 的 finishLogin 用 issuer + Subject 绑定稳定 account_id；Name/Email 只是可更新资料，
// Google access token 和 ID token 都不会随该对象落库或返回浏览器。
type GoogleIdentity struct {
	Subject, Name, Email string
	EmailVerified        bool
}
type GoogleConfig struct{ Issuer, Version, RedirectURI string }
type GoogleIdentityProvider interface {
	Settings() GoogleConfig
	AuthorizationURL(state, nonce, verifier string) string
	Exchange(context.Context, string, string, []byte) (GoogleIdentity, error)
}
type Secrets interface {
	RandomToken() string
	Digest(string) []byte
	Seal(value, purpose string) (string, error)
	Open(value, purpose string) (string, error)
}
type ActorTokens interface {
	Sign(domain.Principal, Target) (string, error)
	Verify(raw string, target Target) (domain.Principal, error)
}
type Target struct {
	Operation        domain.Operation
	Audience, Method string
}
