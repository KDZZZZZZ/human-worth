package security

import (
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"github.com/golang-jwt/jwt/v5"
	"time"
)

// 【阅读 3】actorClaims 是仅在服务间传递的短期身份断言；原始 Cookie/MCP token 不进入业务服务。
// 完整通路：gateway/http.go 的 actorFor → ResolvePrincipal 查 accounts/credentials 并签发
// → content/server.go 的 actor 调用 VerifyActor → verify 再查当前凭据 → 返回可信 Principal。
// 断言有效期最多 30 秒且绑定单一受众和方法；jti 只标识断言，没有一次性消费表，写入幂等由业务层处理。
// 【阅读 3.1】标准声明承载签发者、稳定 account_id、唯一受众、签发/过期时间和随机 jti。
// 【阅读 3.2】Method 是允许执行的完整 RPC 名，不能拿读取断言调用写入方法。
// 【阅读 3.3】CredentialID 用于验证阶段回查已签发凭据，而非重新携带原始秘密。
// 【阅读 3.4】Kind 区分匿名、web 会话和 MCP；Role 与 Version 必须和数据库当前账号状态一致。
type actorClaims struct {
	jwt.RegisteredClaims
	Method       string `json:"method"`
	CredentialID string `json:"credential_id"`
	Kind         int32  `json:"kind"`
	Role         int32  `json:"role"`
	Version      int64  `json:"auth_version"`
}
type Tokens struct{ signing KeyRing }

func NewTokens(ring KeyRing) (*Tokens, error) {
	if err := ring.Validate(); err != nil {
		return nil, err
	}
	return &Tokens{signing: ring}, nil
}
func (s *Tokens) Sign(p domain.Principal, target application.Target) (string, error) {
	now := time.Now()
	claims := actorClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "human-worth.identity", Subject: p.AccountID, Audience: jwt.ClaimStrings{target.Audience}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(30 * time.Second)), ID: RandomToken()}, Method: target.Method, CredentialID: p.CredentialID, Kind: int32(p.ClientKind), Role: int32(p.Role), Version: p.AuthVersion}
	key, err := s.signing.key(s.signing.Active)
	if err != nil {
		return "", err
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	token.Header["kid"] = s.signing.Active
	return token.SignedString(key)
}
func (s *Tokens) Verify(raw string, target application.Target) (domain.Principal, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return domain.Principal{}, domain.ErrInvalidActor
	}
	var claims actorClaims
	parsed, err := jwt.ParseWithClaims(raw, &claims, func(token *jwt.Token) (any, error) {
		kid, ok := token.Header["kid"].(string)
		if !ok {
			return nil, errors.New("missing key id")
		}
		return s.signing.key(kid)
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("human-worth.identity"), jwt.WithAudience(target.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil || !parsed.Valid || claims.IssuedAt == nil || claims.ID == "" || claims.Method != target.Method || len(claims.Audience) != 1 || claims.ExpiresAt.Sub(claims.IssuedAt.Time) > 30*time.Second {
		return domain.Principal{}, domain.ErrInvalidActor
	}
	p := domain.Principal{AccountID: claims.Subject, CredentialID: claims.CredentialID, ClientKind: domain.ClientKind(claims.Kind), Role: domain.Role(claims.Role), AuthVersion: claims.Version}
	if err := p.Validate(); err != nil {
		return domain.Principal{}, err
	}
	return p, nil
}
