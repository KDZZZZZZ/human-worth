package application

import (
	"context"
	"errors"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

// Reconcile 推进持久化待办。外部 RPC 在事务外执行，回写时比较版本以阻止取消竞态。
func (s *Service) Reconcile(ctx context.Context) error {
	ids, err := s.reader.PendingRuns(ctx)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := s.reconcileOne(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) reconcileOne(ctx context.Context, runID string) error {
	r, err := s.read(ctx, runID)
	if err != nil {
		return err
	}
	if domain.Terminal(r.Run.Status) {
		return nil
	}
	if !r.Deadline.After(r.Now) && r.Run.Status != "cancelling" && r.Run.Status != "finalizing" {
		return s.mutate(ctx, runID, func(_ Tx, current *Record) error {
			if current.Version == r.Version {
				domain.Finish(current.State, "failed", "deadline_exceeded")
			}
			return nil
		})
	}
	if r.Pending == "" {
		if r.Assignment != nil && r.Assignment.Attempt != nil && !domain.TimeValue(r.Assignment.LeaseExpiresAt).After(r.Now) {
			return s.mutate(ctx, runID, func(_ Tx, current *Record) error {
				if current.Version != r.Version {
					return nil
				}
				if current.GrantHash != "" {
					domain.Finish(current.State, "failed", "attempt_outcome_unknown")
				} else {
					current.Assignment.Attempt = nil
					current.Assignment.LeaseExpiresAt = nil
				}
				return nil
			})
		}
		return nil
	}
	next := domain.CloneState(r.State)
	switch r.Pending {
	case "open":
		var response *dto.OpenRunRegistrationResponse
		response, err = s.content.OpenRunRegistration(ctx, &dto.OpenRunRegistrationRequest{RunId: runID, TaskId: r.Run.TaskId})
		if err == nil {
			if response.State != "open" {
				err = domain.Conflict("registration_closed")
			} else {
				next.Run.Status = "active"
				next.Run.Stage = "optimizing_ranker"
				next.Pending = ""
			}
		}
	case "validation":
		err = s.validationBatch(ctx, next)
		if err == nil {
			next.Pending = ""
			next.Training = nil
			next.TrainingInputs = nil
			next.TrainingRankings = nil
		}
	case "training":
		var inputs []*domain.RankingSample
		var batch *domain.PreferenceBatch
		batch, inputs, err = s.batch(ctx, next, "training")
		if err == nil {
			s.engine.SetTraining(next, batch, inputs)
			next.Pending = ""
			err = s.checkInputs(ctx, next, next.Assignment)
		}
	case "rank":
		var input *domain.RankingSample
		var ref *domain.SnapshotRef
		input, ref, err = s.readSample(ctx, &domain.SnapshotRef{TaskId: r.Run.TaskId, TaskRevision: r.Task.Revision, CommentCutoff: domain.TimePtr(time.Now())})
		if err == nil {
			err = s.engine.RankGeneratedWork(next, input, ref)
		}
		if err == nil {
			next.Pending = ""
			err = s.checkInputs(ctx, next, next.Assignment)
		}
	case "register":
		_, err = s.register(ctx, next)
		if err == nil {
			domain.Finish(next, "completed", "")
		}
	case "close":
		var response *dto.CloseRunRegistrationResponse
		response, err = s.content.CloseRunRegistration(ctx, &dto.CloseRunRegistrationRequest{RunId: runID, TaskId: r.Run.TaskId})
		if err == nil && response.State != "closed" {
			err = domain.Conflict("registration_not_closed")
		}
		if err == nil {
			for _, c := range next.Run.Candidates {
				var receipt *dto.GetChallengeRegistrationResponse
				receipt, err = s.content.GetChallengeRegistration(ctx, &dto.GetChallengeRegistrationRequest{RunId: runID, CandidateId: c.Id})
				if domain.ErrorCode(err) == domain.NotFound {
					err = nil
					c.RegistrationState = "discarded"
					continue
				}
				if err != nil {
					break
				}
				if !validReceipt(receipt.Receipt, runID, c.Id) {
					err = domain.UnavailableError()
					break
				}
				c.RegistrationState = "registered"
				c.EntryId = receipt.Receipt.EntryId
				c.ReviewId = receipt.Receipt.ReviewId
			}
		}
		if err == nil {
			next.Run.Status = next.TerminalTarget
			next.Run.Stage = "finished"
			next.Pending = ""
			next.Assignment = nil
			next.GrantHash = ""
			next.Training = nil
			next.TrainingInputs = nil
			next.TrainingRankings = nil
		}
	default:
		return domain.UnavailableError()
	}
	if err != nil {
		code := domain.ErrorCode(err)
		if r.Pending != "close" && (code == domain.FailedPrecondition || code == domain.InvalidArgument || code == domain.PermissionDenied || code == domain.NotFound) {
			return s.mutate(ctx, runID, func(_ Tx, current *Record) error {
				if current.Version != r.Version {
					return nil
				}
				reason := "material_unavailable"
				if r.Pending == "validation" || r.Pending == "training" {
					reason = "preference_data_unavailable"
					current.Run.RankerFit.Status = "insufficient_data"
					current.Run.RankerFit.Score = nil
				}
				domain.Finish(current.State, "failed", reason)
				return nil
			})
		}
		return domain.UnavailableError()
	}
	return s.mutate(ctx, runID, func(_ Tx, current *Record) error {
		if current.Version != r.Version {
			return nil
		}
		current.State = next
		if domain.Terminal(next.Run.Status) {
			current.Run.EndedAt = domain.TimePtr(current.Now)
		}
		return nil
	})
}

func validReceipt(r *domain.RegistrationReceipt, run, candidate string) bool {
	return r != nil && r.RunId == run && r.CandidateId == candidate && domain.ValidID(r.EntryId) && (r.ReviewState == "pending_review" || r.ReviewState == "approved" || r.ReviewState == "rejected" || r.ReviewState == "approval_revoked")
}

func (s *Service) register(ctx context.Context, st *domain.State) (*domain.RegistrationReceipt, error) {
	if st.Run.Status != "active" || st.Run.Stage != "registering" || st.Feedback.GetWork() == nil {
		return nil, domain.Conflict("candidate_not_registrable")
	}
	work := st.Feedback.Work
	got, err := s.content.GetChallengeRegistration(ctx, &dto.GetChallengeRegistrationRequest{RunId: st.Run.Id, CandidateId: work.Id})
	var receipt *domain.RegistrationReceipt
	if err == nil {
		receipt = got.Receipt
		if !validReceipt(receipt, st.Run.Id, work.Id) {
			return nil, domain.UnavailableError()
		}
	} else if domain.ErrorCode(err) != domain.NotFound {
		return nil, domain.UnavailableError()
	}
	if receipt == nil {
		a := s.engine.ExplainOwnWork(st)
		if err = s.checkInputs(ctx, st, a); err != nil {
			return nil, err
		}
		created, e := s.content.RegisterChallengeCandidate(ctx, &dto.RegisterChallengeCandidateRequest{RunId: st.Run.Id, CandidateId: work.Id, TaskId: st.Run.TaskId, SourceAttemptId: st.SourceAttemptId, Work: work, ExecutorConfigHash: s.fingerprints.Executor(st.Run.Configuration.Executor)})
		if e != nil {
			return nil, e
		}
		receipt = created.Receipt
	}
	if !validReceipt(receipt, st.Run.Id, work.Id) {
		return nil, domain.UnavailableError()
	}
	for _, c := range st.Run.Candidates {
		if c.Id == work.Id {
			c.RegistrationState = "registered"
			c.EntryId = receipt.EntryId
			c.ReviewId = receipt.ReviewId
		} else {
			c.RegistrationState = "discarded"
		}
	}
	return receipt, nil
}
