package challenge

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identity "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Config 由运维提供能力白名单，管理员请求不能指定供应商地址、镜像或密钥。
type Config struct {
	Models, Harnesses, Skills     map[string]bool
	Lease, RunTimeout             time.Duration
	ModelCallLimit, ToolCallLimit int32
	InputPolicy                   string
}

// Server 只持有 Challenge 数据库与生成的依赖客户端，不跨 schema 读写。
type Server struct {
	pb.UnimplementedChallengeServiceServer
	DB       *pgxpool.Pool
	Config   Config
	Identity identity.IdentityServiceClient
	Content  content.ContentServiceClient
	Asset    asset.AssetServiceClient
	Voting   voting.VotingServiceClient
}

// NewServer 固定一次进程所支持的能力和循环上限，变更策略不会沿用旧拟合记录。
func NewServer(db *pgxpool.Pool, cfg Config, i identity.IdentityServiceClient, c content.ContentServiceClient, a asset.AssetServiceClient, v voting.VotingServiceClient) (*Server, error) {
	if db == nil || i == nil || c == nil || a == nil || v == nil || len(cfg.Models) == 0 || len(cfg.Harnesses) == 0 {
		return nil, errors.New("challenge dependencies and allowlists required")
	}
	if cfg.Lease == 0 {
		cfg.Lease = time.Minute
	}
	if cfg.RunTimeout == 0 {
		cfg.RunTimeout = 2 * time.Hour
	}
	if cfg.ModelCallLimit == 0 {
		cfg.ModelCallLimit = 8
	}
	if cfg.ToolCallLimit == 0 {
		cfg.ToolCallLimit = 16
	}
	if cfg.InputPolicy == "" {
		cfg.InputPolicy = InputPolicyVersion
	}
	if cfg.Lease <= 0 || cfg.RunTimeout <= cfg.Lease || cfg.ModelCallLimit < 1 || cfg.ToolCallLimit < 1 {
		return nil, errors.New("invalid challenge limits")
	}
	return &Server{DB: db, Config: cfg, Identity: i, Content: c, Asset: a, Voting: v}, nil
}

var adminMethods = map[string]bool{
	pb.ChallengeService_StartRun_FullMethodName: true, pb.ChallengeService_GetRun_FullMethodName: true,
	pb.ChallengeService_ListRuns_FullMethodName: true, pb.ChallengeService_GetRunSummary_FullMethodName: true,
	pb.ChallengeService_CancelRun_FullMethodName: true, pb.ChallengeService_RestartRun_FullMethodName: true,
	pb.ChallengeService_RegisterCandidate_FullMethodName: true,
}

// Authorization 先检查 mTLS 服务身份；管理员断言还须在业务方法内向 Identity 验证。
func Authorization(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	caller, err := platform.ServiceName(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if adminMethods[info.FullMethod] {
		if caller != "gateway" {
			return nil, denied("service_forbidden")
		}
	} else if caller != "challenge-worker" && !(strings.HasPrefix(info.FullMethod, "/grpc.health.") && caller == "gateway") {
		return nil, denied("service_forbidden")
	}
	return next(ctx, req)
}
func AuthorizationStream(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	_, err := Authorization(stream.Context(), nil, &grpc.UnaryServerInfo{FullMethod: info.FullMethod}, func(context.Context, any) (any, error) { return nil, next(server, stream) })
	return err
}
func (s *Server) admin(ctx context.Context, assertion, method string) (string, error) {
	if assertion == "" {
		return "", status.Error(codes.Unauthenticated, "administrator_required")
	}
	v, err := s.Identity.VerifyActor(ctx, &identity.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return "", err
	}
	if v.Principal == nil || v.Principal.Role != identity.Role_ROLE_ADMIN || v.Principal.ClientKind != identity.ClientKind_CLIENT_KIND_WEB_SESSION || v.Principal.AccountId == "" {
		return "", denied("administrator_required")
	}
	return v.Principal.AccountId, nil
}

func validID(v string) bool {
	if len(v) < 1 || len(v) > 128 {
		return false
	}
	for _, c := range v {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}
func bounded(v string, n int) bool { return strings.TrimSpace(v) != "" && len(v) <= n }

// validateConfig 冻结初始包和三角色能力，客户端只能选择运维已启用的配置。
func (s *Server) validateConfig(c *pb.RunConfiguration) error {
	if c == nil || c.InitialTaskPackage == nil || c.Packer == nil || c.Ranker == nil || c.Executor == nil || c.Budget == nil {
		return invalid("configuration_required")
	}
	p := c.InitialTaskPackage
	if p.TaskRevision < 1 || !bounded(p.Description, 16384) || !bounded(p.WorkRequirements, 8192) || len(p.TaskAttachmentIds) > 32 {
		return invalid("invalid_initial_package")
	}
	seen := map[string]bool{}
	for _, id := range p.TaskAttachmentIds {
		if !validID(id) || seen[id] {
			return invalid("invalid_task_attachment")
		}
		seen[id] = true
	}
	if math.IsNaN(c.RankerFitThreshold) || c.RankerFitThreshold <= 0 || c.RankerFitThreshold >= 1 || c.RankerMinComparablePairs < 1 || c.RankerRoundLimit < 1 || c.RankerRoundLimit > 100 || c.RoundLimit < 1 || c.RoundLimit > 100 {
		return invalid("invalid_iteration_limits")
	}
	if c.Budget.Unit != "model_calls" || c.Budget.Amount < 1 || c.Budget.Amount > 100000 {
		return invalid("unsupported_budget")
	}
	for _, m := range []*pb.ModelConfiguration{c.Packer, c.Ranker, {Model: c.Executor.Model, Prompt: c.Executor.Prompt, Parameters: c.Executor.Parameters}} {
		if !s.Config.Models[m.Model] || len(m.Prompt) > 16384 {
			return invalid("model_not_allowed")
		}
		for k, v := range m.Parameters.GetFields() {
			if v == nil {
				return invalid("invalid_model_parameters")
			}
			n, ok := v.Kind.(*structpb.Value_NumberValue)
			if !ok || math.IsNaN(n.NumberValue) || math.IsInf(n.NumberValue, 0) {
				return invalid("invalid_model_parameters")
			}
			switch k {
			case "temperature":
				if n.NumberValue < 0 || n.NumberValue > 2 {
					return invalid("invalid_model_parameters")
				}
			case "top_p":
				if n.NumberValue <= 0 || n.NumberValue > 1 {
					return invalid("invalid_model_parameters")
				}
			default:
				return invalid("invalid_model_parameters")
			}
		}
	}
	if !s.Config.Harnesses[c.Executor.Harness] {
		return invalid("harness_not_allowed")
	}
	for _, v := range c.Executor.Skills {
		if !s.Config.Skills[v] {
			return invalid("skill_not_allowed")
		}
	}
	return nil
}

// start 用账号、操作和幂等键唯一受理；重复请求先查回，不重复准备或运行模型。
func (s *Server) start(ctx context.Context, actor, task, parent, operation, key string, cfg *pb.RunConfiguration) (*pb.Run, error) {
	if !validID(task) || !validID(key) {
		return nil, invalid("invalid_request")
	}
	if err := s.validateConfig(cfg); err != nil {
		return nil, err
	}
	digest := hash([]byte(task + "\n" + parent + "\n" + messageHash(cfg)))
	lookup := func() (*pb.Run, error) {
		var storedHash string
		var raw []byte
		err := s.DB.QueryRow(ctx, `SELECT request_hash,state FROM challenge.runs WHERE administrator_id=$1 AND operation=$2 AND idempotency_key=$3`, actor, operation, key).Scan(&storedHash, &raw)
		if err != nil {
			return nil, err
		}
		if storedHash != digest {
			return nil, status.Error(codes.Aborted, "idempotency_conflict")
		}
		st := &pb.StoredRun{}
		if proto.Unmarshal(raw, st) != nil {
			return nil, unavailable()
		}
		return st.Run, nil
	}
	if previous, err := lookup(); !errors.Is(err, pgx.ErrNoRows) {
		return previous, storage(err)
	}
	runID := id("run_")
	st := &pb.StoredRun{Run: &pb.Run{Id: runID, TaskId: task, ParentRunId: parent, AdministratorId: actor, Status: "queued", Stage: "preparing_materials", Configuration: proto.Clone(cfg).(*pb.RunConfiguration), RankerFit: &pb.RankerFit{Status: "pending"}}, Pending: "open", RankerPrompt: cfg.Ranker.Prompt}
	if st.RankerPrompt == "" {
		st.RankerPrompt = "按照原始任务要求比较所有作品，给出完整排名和可核验的作品依据。"
	}
	if err := s.prepare(ctx, st); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	var now time.Time
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return nil, storage(err)
	}
	st.Run.CreatedAt = timestamppb.New(now)
	st.Run.UpdatedAt = timestamppb.New(now)
	data, err := proto.Marshal(st)
	if err != nil {
		return nil, unavailable()
	}
	result, err := tx.Exec(ctx, `INSERT INTO challenge.runs(id,task_id,administrator_id,parent_run_id,operation,idempotency_key,request_hash,status,pending,deadline,state) VALUES($1,$2,$3,$4,$5,$6,$7,'queued','open',$8,$9) ON CONFLICT(administrator_id,operation,idempotency_key) DO NOTHING`, runID, task, actor, parent, operation, key, digest, now.Add(s.Config.RunTimeout), data)
	if err != nil {
		return nil, storage(err)
	}
	if result.RowsAffected() == 0 {
		tx.Rollback(ctx)
		p, e := lookup()
		return p, storage(e)
	}
	if err = audit(ctx, tx, runID, actor, operation, ""); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return st.Run, nil
}

// StartRun 先验证管理员与材料，再幂等受理，不在 HTTP 请求中直接运行模型。
func (s *Server) StartRun(ctx context.Context, q *pb.StartRunRequest) (*pb.StartRunResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_StartRun_FullMethodName)
	if err != nil {
		return nil, err
	}
	r, err := s.start(ctx, actor, q.TaskId, "", "start", q.IdempotencyKey, q.Configuration)
	return &pb.StartRunResponse{Run: r}, err
}

// GetRun 只返回管理员摘要，内部标签、授权和当前材料留在 StoredRun。
func (s *Server) GetRun(ctx context.Context, q *pb.GetRunRequest) (*pb.GetRunResponse, error) {
	if _, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_GetRun_FullMethodName); err != nil {
		return nil, err
	}
	r, err := s.read(ctx, q.RunId)
	if err != nil {
		return nil, err
	}
	return &pb.GetRunResponse{Run: r.Run}, nil
}
func (s *Server) read(ctx context.Context, runID string) (*record, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	return load(ctx, tx, runID, false)
}

// ListRuns 使用创建时间和 ID 稳定分页，不随续租更新时间改变列表位置。
func (s *Server) ListRuns(ctx context.Context, q *pb.ListRunsRequest) (*pb.ListRunsResponse, error) {
	if _, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_ListRuns_FullMethodName); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 || q.Cursor != "" && !validID(q.Cursor) {
		return nil, invalid("invalid_pagination")
	}
	if q.Status != "" && !terminal(q.Status) && q.Status != "queued" && q.Status != "active" && q.Status != "cancelling" && q.Status != "finalizing" {
		return nil, invalid("invalid_status")
	}
	rows, err := s.DB.Query(ctx, `SELECT state FROM challenge.runs WHERE ($1='' OR status=$1) AND ($2='' OR (created_at,id)<(SELECT created_at,id FROM challenge.runs WHERE id=$2)) ORDER BY created_at DESC,id DESC LIMIT $3`, q.Status, q.Cursor, limit+1)
	if err != nil {
		return nil, storage(err)
	}
	defer rows.Close()
	out := &pb.ListRunsResponse{}
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) != nil {
			return nil, unavailable()
		}
		st := &pb.StoredRun{}
		if proto.Unmarshal(b, st) != nil {
			return nil, unavailable()
		}
		out.Items = append(out.Items, st.Run)
	}
	if rows.Err() != nil {
		return nil, unavailable()
	}
	if len(out.Items) > int(limit) {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].Id
	}
	return out, nil
}
func (s *Server) GetRunSummary(ctx context.Context, q *pb.GetRunSummaryRequest) (*pb.GetRunSummaryResponse, error) {
	if _, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_GetRunSummary_FullMethodName); err != nil {
		return nil, err
	}
	out := &pb.GetRunSummaryResponse{}
	err := s.DB.QueryRow(ctx, `SELECT count(*) FILTER(WHERE status IN ('queued','active','cancelling','finalizing')),count(*) FILTER(WHERE status='failed') FROM challenge.runs`).Scan(&out.ActiveRuns, &out.FailedRuns)
	return out, storage(err)
}

// CancelRun 同事务撤销推进权并记录关闭待办，Content 确认关闭后才报告取消完成。
func (s *Server) CancelRun(ctx context.Context, q *pb.CancelRunRequest) (*pb.CancelRunResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_CancelRun_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !bounded(q.Reason, 1024) {
		return nil, invalid("reason_required")
	}
	var out *pb.Run
	err = s.mutate(ctx, q.RunId, func(tx pgx.Tx, r *record) error {
		if r.Run.Status == "cancelled" || r.Run.Status == "cancelling" {
			out = r.Run
			return nil
		}
		if terminal(r.Run.Status) || r.Run.Status == "finalizing" {
			return conflict("run_terminated")
		}
		finish(r, "cancelled", "")
		out = r.Run
		return audit(ctx, tx, r.Run.Id, actor, "cancel", q.Reason)
	})
	return &pb.CancelRunResponse{Run: out}, err
}

// RestartRun 创建独立运行，保留来源关系但不继承旧拟合资格或当前优化提示。
func (s *Server) RestartRun(ctx context.Context, q *pb.RestartRunRequest) (*pb.RestartRunResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_RestartRun_FullMethodName)
	if err != nil {
		return nil, err
	}
	old, err := s.read(ctx, q.ParentRunId)
	if err != nil {
		return nil, err
	}
	if !terminal(old.Run.Status) {
		return nil, conflict("run_not_terminated")
	}
	r, err := s.start(ctx, actor, old.Run.TaskId, old.Run.Id, "restart", q.IdempotencyKey, q.Configuration)
	return &pb.RestartRunResponse{Run: r}, err
}

// RegisterCandidate 优先找回登记回执；终止后仅允许查回，不能补交新作品。
func (s *Server) RegisterCandidate(ctx context.Context, q *pb.RegisterCandidateRequest) (*pb.RegisterCandidateResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, pb.ChallengeService_RegisterCandidate_FullMethodName)
	if err != nil {
		return nil, err
	}
	r, err := s.read(ctx, q.RunId)
	if err != nil {
		return nil, err
	}
	found := false
	for _, c := range r.Run.Candidates {
		if c.Id == q.CandidateId {
			found = true
		}
	}
	if !found {
		return nil, status.Error(codes.NotFound, "candidate_not_found")
	}
	previous, err := s.Content.GetChallengeRegistration(ctx, &content.GetChallengeRegistrationRequest{RunId: q.RunId, CandidateId: q.CandidateId})
	if err == nil {
		if !validReceipt(previous.Receipt, q.RunId, q.CandidateId) {
			return nil, unavailable()
		}
		return &pb.RegisterCandidateResponse{Receipt: previous.Receipt}, nil
	}
	if err != nil && status.Code(err) != codes.NotFound {
		return nil, unavailable()
	}
	err = s.mutate(ctx, q.RunId, func(tx pgx.Tx, r *record) error {
		if r.Run.Status != "active" || r.Run.Stage != "registering" || r.Feedback.GetWork().GetId() != q.CandidateId {
			return conflict("candidate_not_registrable")
		}
		r.Pending = "register"
		return audit(ctx, tx, r.Run.Id, actor, "register", "")
	})
	if err != nil {
		return nil, err
	}
	// 待办已落库，即使响应丢失也由 Reconcile 查回；直接返回本次创建的原始回执。
	receipt, err := s.register(ctx, r.StoredRun)
	if err != nil {
		return nil, err
	}
	return &pb.RegisterCandidateResponse{Receipt: receipt}, nil
}
