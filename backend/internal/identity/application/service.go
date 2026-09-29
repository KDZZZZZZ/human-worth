// Package application 编排稳定账号、Google 登录、会话及 MCP 凭据，并为其他服务验证用户身份。
// 阅读入口：login.go 的登录状态机 → auth.go 的身份解析与断言 → mcp.go 的凭据生命周期；
// 数据表及不变量索引见 repo/postgres/migrate.go，HTTP 入口见 backend/internal/gateway/http.go。
package application

import (
	"context"
	"errors"
	"net/url"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// 【阅读 1】Config 固定公开来源和凭据寿命；密钥、OAuth 与仓储由启动入口分别装配后经端口注入。
type Config struct {
	Origin             string
	SessionTTL, McpTTL time.Duration
}

type Dependencies struct {
	Reader       IdentityReader
	Transactions Transactor
	Maintenance  MaintenanceRepository
	Secrets      Secrets
	Tokens       ActorTokens
	Google       GoogleIdentityProvider
	Targets      []Target
}

// 【阅读 2】Service 经端口组织用例；cmd 装配 gRPC、仓储与外部适配器，跨副本状态统一以 PostgreSQL 为准。
type Service struct {
	config       Config
	reader       IdentityReader
	transactions Transactor
	maintenance  MaintenanceRepository
	secrets      Secrets
	tokens       ActorTokens
	google       GoogleIdentityProvider
	targets      map[string]Target
	operations   map[domain.Operation]Target
}

// 【阅读 3】NewService 校验 HTTPS 来源、操作映射和回调地址，密钥由 security 构造函数校验；补齐会话 24 小时、MCP 30 天的默认寿命。
// OAuth 未配置时仍可提供已有凭据的身份服务；登录入口会返回明确的不可用错误。
func NewService(cfg Config, deps Dependencies) (*Service, error) {
	origin, err := url.Parse(cfg.Origin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, errors.New("PUBLIC_ORIGIN must be an HTTPS origin without a path")
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 24 * time.Hour
	}
	if cfg.McpTTL == 0 {
		cfg.McpTTL = 30 * 24 * time.Hour
	}
	if cfg.SessionTTL <= 0 || cfg.McpTTL <= 0 {
		return nil, errors.New("invalid credential lifetime")
	}
	if deps.Reader == nil || deps.Transactions == nil || deps.Maintenance == nil || deps.Secrets == nil || deps.Tokens == nil {
		return nil, errors.New("missing Identity dependency")
	}
	if deps.Google != nil {
		settings := deps.Google.Settings()
		if settings.RedirectURI != cfg.Origin+"/api/auth/google/callback" || settings.Version == "" || settings.Issuer == "" {
			return nil, errors.New("invalid Google configuration")
		}
	}
	s := &Service{config: cfg, reader: deps.Reader, transactions: deps.Transactions, maintenance: deps.Maintenance, secrets: deps.Secrets, tokens: deps.Tokens, google: deps.Google, targets: make(map[string]Target), operations: make(map[domain.Operation]Target)}
	for _, target := range deps.Targets {
		policy, ok := domain.PolicyFor(target.Operation)
		_, duplicateMethod := s.targets[target.Method]
		_, duplicateOperation := s.operations[target.Operation]
		if !ok || target.Method == "" || target.Audience != policy.Audience || duplicateMethod || duplicateOperation {
			return nil, errors.New("invalid Identity operation binding")
		}
		s.targets[target.Method], s.operations[target.Operation] = target, target
	}
	for _, op := range []domain.Operation{domain.CurrentSession, domain.Logout, domain.CreateMCPToken, domain.ListMCPTokens, domain.RevokeMCPToken} {
		if _, ok := s.operations[op]; !ok {
			return nil, errors.New("missing Identity operation binding")
		}
	}
	return s, nil
}

func (s *Service) newID(prefix string) string { return prefix + "_" + s.secrets.RandomToken() }

// 【阅读 6】audit 在调用方事务内追加操作人、目标和授权版本，不记录原始凭据，也不自行提交。
// login.go、auth.go、mcp.go 的状态变更与此记录一起成功或回滚，避免业务已生效却缺少审计。
func (s *Service) audit(ctx context.Context, tx TxRepos, actor, action, target, reason string, version int64) error {
	return tx.Audit.Append(ctx, domain.AuditEvent{ID: s.newID("audit"), ActorID: actor, Action: action, TargetID: target, Reason: reason, AuthVersion: version})
}
