package google

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/otel"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"strings"
	"time"
)

// 【阅读 1】GoogleIssuer 固定生产身份提供方，禁止请求或环境变量替换为任意 OIDC 站点。
const GoogleIssuer = "https://accounts.google.com"

// 【阅读 2】GoogleOAuth 封装固定提供方的 OAuth 配置、JWKS 验证器和有时限的 HTTP 客户端。
// Version 随客户端配置变更，用于 login.go 的 claim 拒绝旧配置发起的在途流程。
type GoogleOAuth struct {
	config   oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	issuer   string
	Version  string
}

// 【阅读 8】boundedTransport 限制外部 OAuth/JWKS 响应的可读大小，避免依赖返回超大内容。
type boundedTransport struct{ base http.RoundTripper }

// 【阅读 9】RoundTrip 转发请求并将响应体限制为最多 1 MiB，同时保留原始连接关闭能力。
func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	response.Body = &limitedBody{Reader: io.LimitReader(response.Body, 1<<20), Closer: response.Body}
	return response, nil
}

// 【阅读 10】limitedBody 组合限长 Reader 与原响应 Closer，防止包裹响应体后丢失资源释放操作。
type limitedBody struct {
	io.Reader
	io.Closer
}

// 【阅读 4】New 使用固定 Google 授权、换码和证书端点构造生产客户端。
func New(ctx context.Context, clientID, secret, callback, version string) *GoogleOAuth {
	return NewWithEndpoints(ctx, clientID, secret, callback, version, GoogleIssuer,
		"https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", "https://www.googleapis.com/oauth2/v3/certs")
}

// NewWithEndpoints allows an isolated OIDC test provider; production cmd uses New with fixed Google endpoints.
// 【阅读 5】NewWithEndpoints 装配 10 秒超时、禁止跟随重定向的客户端及仅接受 RS256 的 OIDC 验证器。
// 可注入端点仅用于 integration_helpers_test.go 的本地 OIDC 测试；生产入口使用 New。
func NewWithEndpoints(ctx context.Context, clientID, secret, callback, version, issuer, authURL, tokenURL, jwksURL string) *GoogleOAuth {
	client := &http.Client{Timeout: 10 * time.Second, Transport: boundedTransport{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, client), jwksURL)
	return &GoogleOAuth{config: oauth2.Config{ClientID: clientID, ClientSecret: secret, RedirectURL: callback,
		Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams}, Scopes: []string{"openid", "email", "profile"}},
		verifier: oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.RS256}}), client: client, issuer: issuer, Version: version}
}

// 【阅读 6】AuthorizationURL 将 state、nonce 和 PKCE S256 challenge 加入授权 URL，只构造地址、不发起网络请求。
func (o *GoogleOAuth) AuthorizationURL(state, nonce, verifier string) string {
	return o.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// 【阅读 7】Exchange 由 login.go 的 CompleteGoogleLogin 在成功领取流程后调用，使用授权码与 PKCE verifier 换取 ID token。
// 先通过 JWKS 验签及 issuer/audience/有效期校验，再核对该流程的 nonce、非空 subject 和多受众 azp。
// 成功只输出 GoogleIdentity 给 finishLogin；提供方拒绝映射 401，网络、限流或 JWKS 故障映射 503。
// 【阅读 7.1】go-oidc v3.21 将 KeySet 错误包装为文本而未保留类型，这里识别取密钥失败以区别无效登录。
// 【阅读 7.2】显示资料在信任校验后再做长度归一化；超长邮箱舍弃，绝不据此改变稳定账号的绑定规则。
func (o *GoogleOAuth) Exchange(ctx context.Context, code, verifier string, nonceHash []byte) (application.GoogleIdentity, error) {
	ctx, span := otel.Tracer("human-worth.identity").Start(ctx, "google.exchange_and_verify")
	defer span.End()
	token, err := o.config.Exchange(oidc.ClientContext(ctx, o.client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var rejected *oauth2.RetrieveError
		if errors.As(err, &rejected) && rejected.Response != nil && rejected.Response.StatusCode >= 400 && rejected.Response.StatusCode < 500 && rejected.Response.StatusCode != 429 {
			return application.GoogleIdentity{}, application.Reject(application.Unauthenticated, "invalid_google_login")
		}
		return application.GoogleIdentity{}, application.Reject(application.Unavailable, "login_unavailable")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return application.GoogleIdentity{}, application.Reject(application.Unauthenticated, "invalid_google_login")
	}
	id, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		// go-oidc v3.21 wraps KeySet errors as text rather than preserving their type.
		if ctx.Err() != nil || strings.HasPrefix(err.Error(), "failed to verify signature: fetching keys ") {
			return application.GoogleIdentity{}, application.Reject(application.Unavailable, "login_unavailable")
		}
		return application.GoogleIdentity{}, application.Reject(application.Unauthenticated, "invalid_google_login")
	}
	if id.Subject == "" || len(id.Subject) > 255 || id.Nonce == "" || subtle.ConstantTimeCompare(nonceDigest(id.Nonce), nonceHash) != 1 {
		return application.GoogleIdentity{}, application.Reject(application.Unauthenticated, "invalid_google_login")
	}
	var claims struct {
		Name          string `json:"name"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		AZP           string `json:"azp"`
	}
	if id.Claims(&claims) != nil || (len(id.Audience) > 1 && claims.AZP == "") || (claims.AZP != "" && claims.AZP != o.config.ClientID) {
		return application.GoogleIdentity{}, application.Reject(application.Unauthenticated, "invalid_google_login")
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = "Human Worth 用户"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if len(claims.Email) > 320 {
		claims.Email = ""
		claims.EmailVerified = false
	}
	return application.GoogleIdentity{Subject: id.Subject, Name: name, Email: claims.Email, EmailVerified: claims.EmailVerified}, nil
}

func nonceDigest(value string) []byte { sum := sha256.Sum256([]byte(value)); return sum[:] }
func (o *GoogleOAuth) Settings() application.GoogleConfig {
	return application.GoogleConfig{Issuer: o.issuer, Version: o.Version, RedirectURI: o.config.RedirectURL}
}
func (o *GoogleOAuth) Validate() error {
	if o.config.ClientID == "" || o.config.ClientSecret == "" || o.Version == "" {
		return errors.New("invalid Google configuration")
	}
	return nil
}
