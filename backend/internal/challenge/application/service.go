package application

import (
	"context"
	"errors"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

// Service 编排事务和外部能力，迭代规则由领域 Engine 负责。
type Service struct {
	reader        Reader
	transactions  Transactions
	engine        domain.Engine
	administrator Administrator
	content       Content
	asset         Asset
	voting        Voting
	fingerprints  Fingerprints
}
type Dependencies struct {
	Reader        Reader
	Transactions  Transactions
	Administrator Administrator
	Content       Content
	Asset         Asset
	Voting        Voting
	Fingerprints  Fingerprints
}

func NewService(cfg domain.Config, d Dependencies) (*Service, error) {
	if d.Reader == nil || d.Transactions == nil || d.Administrator == nil || d.Content == nil || d.Asset == nil || d.Voting == nil || d.Fingerprints == nil {
		return nil, errors.New("challenge dependencies required")
	}
	engine, err := domain.NewEngine(cfg)
	if err != nil {
		return nil, err
	}
	return &Service{reader: d.Reader, transactions: d.Transactions, engine: engine, administrator: d.Administrator, content: d.Content, asset: d.Asset, voting: d.Voting, fingerprints: d.Fingerprints}, nil
}
func storage(err error) error {
	if err == nil {
		return nil
	}
	if domain.ErrorCode(err) != domain.Unknown {
		return err
	}
	return domain.UnavailableError()
}
func (s *Service) admin(ctx context.Context, assertion, operation string) (string, error) {
	return s.administrator.Verify(ctx, assertion, operation)
}
func (s *Service) read(ctx context.Context, id string) (*Record, error) {
	return s.reader.Read(ctx, id)
}
func (s *Service) mutate(ctx context.Context, id string, run func(Tx, *Record) error) error {
	return s.transactions.WithinTx(ctx, func(tx Tx) error {
		r, err := tx.Load(ctx, id)
		if err != nil {
			return err
		}
		if err = run(tx, r); err != nil {
			return err
		}
		return tx.Save(ctx, r)
	})
}
func (s *Service) start(ctx context.Context, actor, task, parent, operation, key string, cfg *domain.RunConfiguration) (*domain.Run, error) {
	if !domain.ValidID(task) || !domain.ValidID(key) {
		return nil, domain.Invalid("invalid_request")
	}
	if err := s.engine.ValidateConfiguration(cfg); err != nil {
		return nil, err
	}
	lookup := RunKey{Administrator: actor, Operation: operation, Key: key, Hash: domain.Hash([]byte(task + "\n" + parent + "\n" + s.fingerprints.Configuration(cfg)))}
	if previous, err := s.reader.LookupRun(ctx, lookup); !errors.Is(err, ErrNotFound) {
		return previous, storage(err)
	}
	st := &domain.State{Run: &domain.Run{Id: domain.NewID("run_"), TaskId: task, ParentRunId: parent, AdministratorId: actor, Status: "queued", Stage: "preparing_materials", Configuration: domain.CloneRunConfiguration(cfg), RankerFit: &domain.RankerFit{Status: "pending"}}, Pending: "open", RankerPrompt: cfg.Ranker.Prompt}
	if st.RankerPrompt == "" {
		st.RankerPrompt = "按照原始任务要求比较所有作品，给出完整排名和可核验的作品依据。"
	}
	if err := s.prepare(ctx, st); err != nil {
		return nil, err
	}
	inserted := false
	err := s.transactions.WithinTx(ctx, func(tx Tx) error {
		now, err := tx.Now(ctx)
		if err != nil {
			return err
		}
		st.Run.CreatedAt = domain.TimePtr(now)
		st.Run.UpdatedAt = domain.TimePtr(now)
		inserted, err = tx.InsertRun(ctx, lookup, st, now.Add(s.engine.Config.RunTimeout))
		if err != nil || !inserted {
			return err
		}
		return tx.Audit(ctx, st.Run.Id, actor, operation, "")
	})
	if err != nil {
		return nil, err
	}
	if !inserted {
		return s.reader.LookupRun(ctx, lookup)
	}
	return st.Run, nil
}

// StartRun 先验证管理员与材料，再幂等受理，不在 HTTP 请求中直接运行模型。
func (s *Service) StartRun(ctx context.Context, q *dto.StartRunRequest) (*dto.StartRunResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, "StartRun")
	if err != nil {
		return nil, err
	}
	r, err := s.start(ctx, actor, q.TaskId, "", "start", q.IdempotencyKey, q.Configuration)
	return &dto.StartRunResponse{Run: r}, err
}

// GetRun 只返回管理员摘要，内部标签、授权和当前材料留在领域状态。
func (s *Service) GetRun(ctx context.Context, q *dto.GetRunRequest) (*dto.GetRunResponse, error) {
	if _, err := s.admin(ctx, q.ActorAssertion, "GetRun"); err != nil {
		return nil, err
	}
	r, err := s.read(ctx, q.RunId)
	if err != nil {
		return nil, err
	}
	return &dto.GetRunResponse{Run: r.Run}, nil
}

// ListRuns 使用创建时间和 ID 稳定分页，不随续租更新时间改变列表位置。
func (s *Service) ListRuns(ctx context.Context, q *dto.ListRunsRequest) (*dto.ListRunsResponse, error) {
	if _, err := s.admin(ctx, q.ActorAssertion, "ListRuns"); err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 || q.Cursor != "" && !domain.ValidID(q.Cursor) {
		return nil, domain.Invalid("invalid_pagination")
	}
	if q.Status != "" && !domain.Terminal(q.Status) && q.Status != "queued" && q.Status != "active" && q.Status != "cancelling" && q.Status != "finalizing" {
		return nil, domain.Invalid("invalid_status")
	}
	items, err := s.reader.ListRuns(ctx, q.Status, q.Cursor, limit+1)
	if err != nil {
		return nil, err
	}
	out := &dto.ListRunsResponse{Items: items}
	if len(out.Items) > int(limit) {
		out.Items = out.Items[:limit]
		out.NextCursor = out.Items[limit-1].Id
	}
	return out, nil
}
func (s *Service) GetRunSummary(ctx context.Context, q *dto.GetRunSummaryRequest) (*dto.GetRunSummaryResponse, error) {
	if _, err := s.admin(ctx, q.ActorAssertion, "GetRunSummary"); err != nil {
		return nil, err
	}
	return s.reader.Summary(ctx)
}

// CancelRun 同事务撤销推进权并记录关闭待办，Content 确认关闭后才报告取消完成。
func (s *Service) CancelRun(ctx context.Context, q *dto.CancelRunRequest) (*dto.CancelRunResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, "CancelRun")
	if err != nil {
		return nil, err
	}
	if !domain.Bounded(q.Reason, 1024) {
		return nil, domain.Invalid("reason_required")
	}
	var out *domain.Run
	err = s.mutate(ctx, q.RunId, func(tx Tx, r *Record) error {
		if r.Run.Status == "cancelled" || r.Run.Status == "cancelling" {
			out = r.Run
			return nil
		}
		if domain.Terminal(r.Run.Status) || r.Run.Status == "finalizing" {
			return domain.Conflict("run_terminated")
		}
		domain.Finish(r.State, "cancelled", "")
		out = r.Run
		return tx.Audit(ctx, r.Run.Id, actor, "cancel", q.Reason)
	})
	return &dto.CancelRunResponse{Run: out}, err
}

// RestartRun 创建独立运行，保留来源关系但不继承旧拟合资格或当前优化提示。
func (s *Service) RestartRun(ctx context.Context, q *dto.RestartRunRequest) (*dto.RestartRunResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, "RestartRun")
	if err != nil {
		return nil, err
	}
	old, err := s.read(ctx, q.ParentRunId)
	if err != nil {
		return nil, err
	}
	if !domain.Terminal(old.Run.Status) {
		return nil, domain.Conflict("run_not_terminated")
	}
	r, err := s.start(ctx, actor, old.Run.TaskId, old.Run.Id, "restart", q.IdempotencyKey, q.Configuration)
	return &dto.RestartRunResponse{Run: r}, err
}

// RegisterCandidate 优先找回登记回执；终止后仅允许查回，不能补交新作品。
func (s *Service) RegisterCandidate(ctx context.Context, q *dto.RegisterCandidateRequest) (*dto.RegisterCandidateResponse, error) {
	actor, err := s.admin(ctx, q.ActorAssertion, "RegisterCandidate")
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
		return nil, domain.Reject(domain.NotFound, "candidate_not_found")
	}
	previous, err := s.content.GetChallengeRegistration(ctx, &dto.GetChallengeRegistrationRequest{RunId: q.RunId, CandidateId: q.CandidateId})
	if err == nil {
		if !validReceipt(previous.Receipt, q.RunId, q.CandidateId) {
			return nil, domain.UnavailableError()
		}
		return &dto.RegisterCandidateResponse{Receipt: previous.Receipt}, nil
	}
	if err != nil && domain.ErrorCode(err) != domain.NotFound {
		return nil, domain.UnavailableError()
	}
	err = s.mutate(ctx, q.RunId, func(tx Tx, r *Record) error {
		if r.Run.Status != "active" || r.Run.Stage != "registering" || r.Feedback.GetWork().GetId() != q.CandidateId {
			return domain.Conflict("candidate_not_registrable")
		}
		r.Pending = "register"
		return tx.Audit(ctx, r.Run.Id, actor, "register", "")
	})
	if err != nil {
		return nil, err
	}
	// 待办已落库，即使响应丢失也由 Reconcile 查回；直接返回本次创建的原始回执。
	receipt, err := s.register(ctx, r.State)
	if err != nil {
		return nil, err
	}
	return &dto.RegisterCandidateResponse{Receipt: receipt}, nil
}
