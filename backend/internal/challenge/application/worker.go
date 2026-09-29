package application

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

func (s *Service) live(ctx context.Context, r *Record, ref *domain.AttemptRef) error {
	if ref == nil {
		return domain.Invalid("attempt_required")
	}
	owner, err := workerIdentity(ctx, ref.WorkerInstanceId)
	if err != nil {
		return err
	}
	a := r.Assignment
	if r.Run.Status != "active" || a == nil || a.Attempt == nil || *ref != *a.Attempt || r.Worker != owner || !domain.TimeValue(a.LeaseExpiresAt).After(r.Now) || !r.Deadline.After(r.Now) {
		return domain.Conflict("stale_lease")
	}
	return s.checkInputs(ctx, r.State, a)
}

func (s *Service) RenewLease(ctx context.Context, q *dto.RenewLeaseRequest) (*dto.RenewLeaseResponse, error) {
	if q.Attempt == nil {
		return nil, domain.Invalid("attempt_required")
	}
	out := &dto.RenewLeaseResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ Tx, r *Record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		expires := r.Now.Add(s.engine.Config.Lease)
		if expires.After(r.Deadline) {
			expires = r.Deadline
		}
		r.Assignment.LeaseExpiresAt = domain.TimePtr(expires)
		out.ExpiresAt = r.Assignment.LeaseExpiresAt
		return nil
	})
	return out, err
}
func (s *Service) ReportProgress(ctx context.Context, q *dto.ReportProgressRequest) (*dto.ReportProgressResponse, error) {
	if q.Attempt == nil || q.Sequence < 1 || !domain.ValidID(q.Phase) {
		return nil, domain.Invalid("invalid_progress")
	}
	out := &dto.ReportProgressResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ Tx, r *Record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if q.Sequence < r.ProgressSequence || q.Sequence == r.ProgressSequence && q.Phase != r.ProgressPhase {
			return domain.Conflict("progress_conflict")
		}
		r.ProgressSequence = q.Sequence
		r.ProgressPhase = q.Phase
		out.AckSequence = q.Sequence
		return nil
	})
	return out, err
}
func (s *Service) ActivateAttempt(ctx context.Context, q *dto.ActivateAttemptRequest) (*dto.ActivateAttemptResponse, error) {
	if q.Attempt == nil || !domain.ValidID(q.ExecutionInstanceId) {
		return nil, domain.Invalid("invalid_activation")
	}
	out := &dto.ActivateAttemptResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ Tx, r *Record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if r.GrantHash != "" {
			return domain.Conflict("attempt_already_activated")
		}
		out.Grant = domain.NewID("grant_")
		r.GrantHash = domain.Hash([]byte(out.Grant))
		r.ExecutionInstanceId = q.ExecutionInstanceId
		out.ExpiresAt = r.Assignment.Deadline
		return nil
	})
	return out, err
}
func grantOK(r *Record, grant string) bool {
	return grant != "" && r.GrantHash != "" && subtle.ConstantTimeCompare([]byte(domain.Hash([]byte(grant))), []byte(r.GrantHash)) == 1
}
func (s *Service) CheckTaskAccess(ctx context.Context, q *dto.CheckTaskAccessRequest) (*dto.CheckTaskAccessResponse, error) {
	if q.Attempt == nil {
		return nil, domain.Invalid("attempt_required")
	}
	r, err := s.read(ctx, q.Attempt.RunId)
	if err != nil {
		return nil, err
	}
	if err = s.live(ctx, r, q.Attempt); err != nil {
		return nil, err
	}
	if !grantOK(r, q.Grant) {
		return nil, domain.Denied("invalid_grant")
	}
	allowed := q.Operation == "model" && q.ResourceRef != "" && (q.ResourceRef == r.Assignment.Model.GetModel() || q.ResourceRef == r.Assignment.Executor.GetModel())
	if q.Operation == "read" {
		for _, m := range r.Assignment.Materials {
			if m.Id == q.ResourceRef {
				allowed = true
			}
		}
	}
	if !allowed {
		return nil, domain.Denied("resource_forbidden")
	}
	return &dto.CheckTaskAccessResponse{Allowed: true, ExpiresAt: r.Assignment.LeaseExpiresAt}, nil
}

func (s *Service) FailWork(ctx context.Context, q *dto.FailWorkRequest) (*dto.FailWorkResponse, error) {
	if q.Attempt == nil {
		return nil, domain.Invalid("attempt_required")
	}
	allowed := map[string]bool{"model_failed": true, "model_outcome_unknown": true, "invalid_model_result": true, "executor_failed": true, "executor_unavailable": true, "tool_limit_exceeded": true, "material_unavailable": true, "model_budget_exhausted": true}
	if !allowed[q.FailureCode] {
		return nil, domain.Invalid("invalid_failure_code")
	}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ Tx, r *Record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		reason := q.FailureCode
		if !q.OutcomeKnown {
			reason = "attempt_outcome_unknown"
		}
		domain.Finish(r.State, "failed", reason)
		return nil
	})
	return &dto.FailWorkResponse{Accepted: err == nil}, err
}

func materialAccess(r *Record, ref *domain.AttemptRef) *domain.Access {
	return &domain.Access{TaskId: r.Run.TaskId, RunId: r.Run.Id, AttemptId: ref.AttemptId, Purpose: fmt.Sprint(r.Assignment.Kind)}
}

// ClaimWork serializes admission, then locks a Run before recording its lease.
// ponytail: two global leases use one advisory lock; split the queue only if throughput requires it.
func (s *Service) ClaimWork(ctx context.Context, q *dto.ClaimWorkRequest) (*dto.ClaimWorkResponse, error) {
	owner, err := workerIdentity(ctx, q.WorkerInstanceId)
	if err != nil {
		return nil, err
	}
	if !domain.ValidID(q.ClaimRequestId) || len(q.Capabilities) == 0 || len(q.Capabilities) > 7 {
		return nil, domain.Invalid("invalid_claim")
	}
	var kinds []int32
	seen := map[domain.WorkKind]bool{}
	for _, k := range q.Capabilities {
		if k < 1 || k > 7 || seen[k] {
			return nil, domain.Invalid("invalid_capabilities")
		}
		seen[k] = true
		kinds = append(kinds, int32(k))
	}
	out := &dto.ClaimWorkResponse{}
	err = s.transactions.WithinTx(ctx, func(tx Tx) error {
		if err := tx.LockClaims(ctx); err != nil {
			return err
		}
		digest := s.fingerprints.Claim(q)
		previous, err := tx.FindClaim(ctx, owner, q.ClaimRequestId)
		if err == nil {
			if previous.Hash != digest {
				return domain.Conflict("claim_conflict")
			}
			if previous.RunID == "" {
				return nil
			}
			r, err := tx.Load(ctx, previous.RunID)
			if err != nil {
				return err
			}
			if r.Assignment == nil || r.Assignment.Attempt.GetAttemptId() != previous.AttemptID {
				return domain.Conflict("stale_claim")
			}
			if err = s.live(ctx, r, r.Assignment.Attempt); err != nil {
				return err
			}
			out.Assignment = r.Assignment
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		busy, own, err := tx.LeaseUsage(ctx, owner)
		if err != nil {
			return err
		}
		receipt := ClaimReceipt{Hash: digest}
		if busy < 2 && !own {
			runID, err := tx.NextRun(ctx, kinds)
			if err != nil && !errors.Is(err, ErrNotFound) {
				return err
			}
			if runID != "" {
				r, err := tx.Load(ctx, runID)
				if err != nil {
					return err
				}
				if err = s.checkInputs(ctx, r.State, r.Assignment); err != nil {
					if domain.ErrorCode(err) == domain.Unavailable || domain.ErrorCode(err) == domain.DeadlineExceeded {
						return err
					}
					if domain.Reason(err) == "ranker_fit_invalidated" || domain.Reason(err) == "input_policy_changed" {
						r.Run.RankerFit.Status = "invalidated"
					}
					domain.Finish(r.State, "failed", "input_or_fit_invalidated")
				} else {
					r.Epoch++
					attemptID := domain.NewID("attempt_")
					a := r.Assignment
					a.Attempt = &domain.AttemptRef{RunId: runID, WorkItemId: a.WorkItemId, AttemptId: attemptID, LeaseEpoch: r.Epoch, WorkerInstanceId: q.WorkerInstanceId}
					expires := r.Now.Add(s.engine.Config.Lease)
					if expires.After(r.Deadline) {
						expires = r.Deadline
					}
					a.LeaseExpiresAt = domain.TimePtr(expires)
					a.Deadline = domain.TimePtr(r.Deadline)
					r.Worker = owner
					r.GrantHash = ""
					r.ExecutionInstanceId = ""
					r.ProgressSequence = 0
					r.ProgressPhase = ""
					out.Assignment = a
					receipt.RunID = runID
					receipt.AttemptID = attemptID
				}
				if err = tx.Save(ctx, r); err != nil {
					return err
				}
			}
		}
		return tx.AddClaim(ctx, owner, q.ClaimRequestId, receipt)
	})
	return out, err
}

func (s *Service) ReserveModelCall(ctx context.Context, q *dto.ReserveModelCallRequest) (*dto.ReserveModelCallResponse, error) {
	if q.Attempt == nil || !domain.ValidID(q.RequestId) || len(q.RequestDigest) != 64 {
		return nil, domain.Invalid("invalid_model_call")
	}
	out := &dto.ReserveModelCallResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(tx Tx, r *Record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if !grantOK(r, q.Grant) {
			return domain.Denied("invalid_grant")
		}
		if q.Model == "" || q.Model != r.Assignment.Model.GetModel() && q.Model != r.Assignment.Executor.GetModel() {
			return domain.Denied("model_forbidden")
		}
		previous, err := tx.FindModelCall(ctx, q.Attempt.AttemptId, q.RequestId)
		if err == nil {
			if previous.Digest != q.RequestDigest {
				return domain.Conflict("model_call_conflict")
			}
			out.ReservationId = previous.ID
			out.State = previous.State
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		count, unknown, err := tx.ModelCallCounts(ctx, q.Attempt.AttemptId)
		if err != nil {
			return err
		}
		if unknown > 0 {
			return domain.Conflict("model_outcome_unknown")
		}
		if count >= int(r.Assignment.ModelCallLimit) || r.ReservedCalls >= r.Run.Configuration.Budget.Amount {
			return domain.Reject(domain.ResourceExhausted, "model_budget_exhausted")
		}
		out.ReservationId = domain.NewID("call_")
		out.State = "reserved"
		out.DispatchPermit = true
		if err = tx.AddModelCall(ctx, r, q, out.ReservationId); err != nil {
			return err
		}
		r.ReservedCalls++
		return nil
	})
	return out, err
}

func (s *Service) SettleModelCall(ctx context.Context, q *dto.SettleModelCallRequest) (*dto.SettleModelCallResponse, error) {
	if q.Attempt == nil || q.Outcome != "succeeded" && q.Outcome != "failed" && q.Outcome != "unknown" {
		return nil, domain.Invalid("invalid_settlement")
	}
	owner, err := workerIdentity(ctx, q.Attempt.WorkerInstanceId)
	if err != nil {
		return nil, err
	}
	err = s.transactions.WithinTx(ctx, func(tx Tx) error {
		previous, err := tx.LockModelCall(ctx, q, owner)
		if errors.Is(err, ErrNotFound) {
			return domain.Denied("reservation_forbidden")
		}
		if err != nil {
			return err
		}
		if previous != "reserved" && previous != q.Outcome {
			return domain.Conflict("settlement_conflict")
		}
		return tx.SettleModelCall(ctx, q.ReservationId, q.Outcome)
	})
	return &dto.SettleModelCallResponse{State: q.Outcome}, err
}

// CompleteWork keeps receipt, lease fencing and the complete domain transition in
// one transaction. External ownership is rechecked before accepting E's output.
func (s *Service) CompleteWork(ctx context.Context, q *dto.CompleteWorkRequest) (*dto.CompleteWorkResponse, error) {
	if q.Attempt == nil || q.Result == nil || q.ResultDigest != s.fingerprints.Result(q) {
		return nil, domain.Invalid("invalid_result_digest")
	}
	owner, err := workerIdentity(ctx, q.Attempt.WorkerInstanceId)
	if err != nil {
		return nil, err
	}
	err = s.mutate(ctx, q.Attempt.RunId, func(tx Tx, r *Record) error {
		previous, err := tx.FindResult(ctx, q.Attempt)
		if err == nil {
			if previous.Digest != q.ResultDigest || previous.Worker != owner || previous.Epoch != q.Attempt.LeaseEpoch || previous.WorkID != q.Attempt.WorkItemId {
				return domain.Conflict("result_conflict")
			}
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		if err = s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if r.GrantHash == "" {
			return domain.Conflict("attempt_not_activated")
		}
		_, unresolved, err := tx.ModelCallCounts(ctx, q.Attempt.AttemptId)
		if err != nil {
			return err
		}
		if unresolved != 0 {
			return domain.Conflict("model_outcome_unknown")
		}
		if r.Assignment.Kind == domain.WorkKind_WORK_KIND_EXECUTE_PACKAGE {
			g := q.GetGenerated()
			if g == nil || !domain.ValidWork(g.Work) || g.Work.Id != "work_"+q.Attempt.AttemptId || !domain.Bounded(g.Report, 16384) {
				return domain.Invalid("invalid_generated_work")
			}
			for _, part := range g.Work.Artifacts {
				if f := part.GetFile(); f != nil {
					response, err := s.asset.GetAsset(ctx, &dto.GetAssetRequest{AssetId: f.Id, Access: &domain.Access{TaskId: r.Run.TaskId, RunId: r.Run.Id, AttemptId: q.Attempt.AttemptId, Purpose: "output"}})
					if err != nil {
						return domain.UnavailableError()
					}
					if response.Asset == nil || *response.Asset != *f || f.RunId != r.Run.Id || f.AttemptId != q.Attempt.AttemptId {
						return domain.Denied("artifact_not_owned")
					}
				}
			}
		}
		result := domain.Result{Prompt: q.GetPrompt(), Rankings: q.GetRankings(), Generated: q.GetGenerated(), Judgment: q.GetJudgment()}
		if err = s.engine.ApplyResult(r.State, result, q.Attempt.AttemptId, s.fitBinding(r.State), r.Now); err != nil {
			return err
		}
		return tx.AddResult(ctx, q.Attempt, owner, q.ResultDigest)
	})
	return &dto.CompleteWorkResponse{Accepted: err == nil}, err
}
