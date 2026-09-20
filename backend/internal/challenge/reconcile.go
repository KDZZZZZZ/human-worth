package challenge

import (
	"context"
	"errors"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reconcile 推进持久化待办。外部 RPC 在事务外执行，回写时比较版本以阻止取消竞态。
func (s *Server) Reconcile(ctx context.Context) error {
	rows, err := s.DB.Query(ctx, `SELECT id FROM challenge.runs WHERE status IN ('queued','active','cancelling','finalizing') ORDER BY updated_at,id LIMIT 50`)
	if err != nil {
		return storage(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return storage(err)
	}
	if err = rows.Err(); err != nil {
		return storage(err)
	}
	var failures []error
	for _, id := range ids {
		if err := s.reconcileOne(ctx, id); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Server) reconcileOne(ctx context.Context, runID string) error {
	r, err := s.read(ctx, runID)
	if err != nil {
		return err
	}
	if terminal(r.Run.Status) {
		return nil
	}
	if !r.deadline.After(r.now) && r.Run.Status != "cancelling" && r.Run.Status != "finalizing" {
		return s.mutate(ctx, runID, func(_ pgx.Tx, current *record) error {
			if current.version == r.version {
				finish(current, "failed", "deadline_exceeded")
			}
			return nil
		})
	}
	if r.Pending == "" {
		if r.Assignment != nil && r.Assignment.Attempt != nil && !r.Assignment.LeaseExpiresAt.AsTime().After(r.now) {
			return s.mutate(ctx, runID, func(_ pgx.Tx, current *record) error {
				if current.version != r.version {
					return nil
				}
				if current.GrantHash != "" {
					finish(current, "failed", "attempt_outcome_unknown")
				} else {
					current.Assignment.Attempt = nil
					current.Assignment.LeaseExpiresAt = nil
				}
				return nil
			})
		}
		return nil
	}
	next := proto.Clone(r.StoredRun).(*pb.StoredRun)
	switch r.Pending {
	case "open":
		var response *content.OpenRunRegistrationResponse
		response, err = s.Content.OpenRunRegistration(ctx, &content.OpenRunRegistrationRequest{RunId: runID, TaskId: r.Run.TaskId})
		if err == nil {
			if response.State != "open" {
				err = conflict("registration_closed")
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
		var inputs []*pb.RankingSample
		next.Training, inputs, err = s.batch(ctx, next, "training")
		if err == nil {
			next.TrainingInputs = inputs
			a := s.assignment(next, pb.WorkKind_WORK_KIND_TRAIN_RANKER)
			a.Input = &pb.Assignment_Rank{Rank: &pb.RankInput{Samples: inputs}}
			a.Materials = rankMaterials(inputs)
			next.Assignment = a
			next.Pending = ""
			err = s.checkInputs(ctx, next, a)
		}
	case "rank":
		var input *pb.RankingSample
		var ref *content.SnapshotRef
		input, ref, err = s.readSample(ctx, &content.SnapshotRef{TaskId: r.Run.TaskId, TaskRevision: r.Task.Revision, CommentCutoff: timestamppb.Now()})
		if err == nil {
			next.Target = ref
			next.Works = input.Works
			next.Comments = input.Comments
			for _, w := range input.Works {
				if w.Id == next.Feedback.Work.Id {
					err = conflict("generated_work_id_conflict")
				}
			}
			if err == nil {
				input.Works = append(input.Works, next.Feedback.Work)
				a := s.assignment(next, pb.WorkKind_WORK_KIND_RANK_WORKS)
				a.Input = &pb.Assignment_Rank{Rank: &pb.RankInput{Samples: []*pb.RankingSample{input}}}
				a.Materials = rankMaterials([]*pb.RankingSample{input})
				for _, m := range a.Materials {
					if m.SourceId == next.Feedback.Work.Id {
						m.SourceKind = "own_work"
					}
				}
				next.Assignment = a
				next.Pending = ""
				err = s.checkInputs(ctx, next, a)
			}
		}
	case "register":
		_, err = s.register(ctx, next)
		if err == nil {
			temp := &record{StoredRun: next}
			finish(temp, "completed", "")
		}
	case "close":
		var response *content.CloseRunRegistrationResponse
		response, err = s.Content.CloseRunRegistration(ctx, &content.CloseRunRegistrationRequest{RunId: runID, TaskId: r.Run.TaskId})
		if err == nil && response.State != "closed" {
			err = conflict("registration_not_closed")
		}
		if err == nil {
			for _, c := range next.Run.Candidates {
				var receipt *content.GetChallengeRegistrationResponse
				receipt, err = s.Content.GetChallengeRegistration(ctx, &content.GetChallengeRegistrationRequest{RunId: runID, CandidateId: c.Id})
				if status.Code(err) == codes.NotFound {
					err = nil
					c.RegistrationState = "discarded"
					continue
				}
				if err != nil {
					break
				}
				if !validReceipt(receipt.Receipt, runID, c.Id) {
					err = unavailable()
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
		return unavailable()
	}
	if err != nil {
		code := status.Code(err)
		if r.Pending != "close" && (code == codes.FailedPrecondition || code == codes.InvalidArgument || code == codes.PermissionDenied || code == codes.NotFound) {
			return s.mutate(ctx, runID, func(_ pgx.Tx, current *record) error {
				if current.version != r.version {
					return nil
				}
				reason := "material_unavailable"
				if r.Pending == "validation" || r.Pending == "training" {
					reason = "preference_data_unavailable"
					current.Run.RankerFit.Status = "insufficient_data"
					current.Run.RankerFit.Score = nil
				}
				finish(current, "failed", reason)
				return nil
			})
		}
		return unavailable()
	}
	return s.mutate(ctx, runID, func(_ pgx.Tx, current *record) error {
		if current.version != r.version {
			return nil
		}
		current.StoredRun = next
		if terminal(next.Run.Status) {
			current.Run.EndedAt = timestamppb.New(current.now)
		}
		return nil
	})
}

func validReceipt(r *content.RegistrationReceipt, run, candidate string) bool {
	return r != nil && r.RunId == run && r.CandidateId == candidate && validID(r.EntryId) && (r.ReviewState == "pending_review" || r.ReviewState == "approved" || r.ReviewState == "rejected" || r.ReviewState == "approval_revoked")
}

func (s *Server) register(ctx context.Context, st *pb.StoredRun) (*content.RegistrationReceipt, error) {
	if st.Run.Status != "active" || st.Run.Stage != "registering" || st.Feedback.GetWork() == nil {
		return nil, conflict("candidate_not_registrable")
	}
	work := st.Feedback.Work
	got, err := s.Content.GetChallengeRegistration(ctx, &content.GetChallengeRegistrationRequest{RunId: st.Run.Id, CandidateId: work.Id})
	var receipt *content.RegistrationReceipt
	if err == nil {
		receipt = got.Receipt
		if !validReceipt(receipt, st.Run.Id, work.Id) {
			return nil, unavailable()
		}
	} else if status.Code(err) != codes.NotFound {
		return nil, unavailable()
	}
	if receipt == nil {
		a := s.assignment(st, pb.WorkKind_WORK_KIND_EXPLAIN_OWN_WORK)
		a.Input = &pb.Assignment_Explain{Explain: &pb.ExplainInput{TaskDescription: st.Task.Description, OwnWork: work}}
		a.Materials = append(selectedMaterials(st), workMaterials(st.Run.TaskId, work, true)...)
		if err = s.checkInputs(ctx, st, a); err != nil {
			return nil, err
		}
		created, e := s.Content.RegisterChallengeCandidate(ctx, &content.RegisterChallengeCandidateRequest{RunId: st.Run.Id, CandidateId: work.Id, TaskId: st.Run.TaskId, SourceAttemptId: st.SourceAttemptId, Work: work, ExecutorConfigHash: messageHash(st.Run.Configuration.Executor)})
		if e != nil {
			return nil, e
		}
		receipt = created.Receipt
	}
	if !validReceipt(receipt, st.Run.Id, work.Id) {
		return nil, unavailable()
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
