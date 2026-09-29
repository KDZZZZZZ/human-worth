package challenge

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// workerIdentity 将实例名绑定到已验证的客户端证书，不能仅凭请求字段冒充另一个 worker。
func workerIdentity(ctx context.Context, instance string) (string, error) {
	service, err := platform.ServiceName(ctx)
	if err != nil || service != "challenge-worker" || !validID(instance) {
		return "", denied("worker_identity_required")
	}
	p, _ := peer.FromContext(ctx)
	info := p.AuthInfo.(credentials.TLSInfo)
	return hash(info.State.PeerCertificates[0].Raw) + "/" + instance, nil
}
func (s *Server) live(ctx context.Context, r *record, ref *pb.AttemptRef) error {
	if ref == nil {
		return invalid("attempt_required")
	}
	owner, err := workerIdentity(ctx, ref.WorkerInstanceId)
	if err != nil {
		return err
	}
	a := r.Assignment
	if r.Run.Status != "active" || a == nil || a.Attempt == nil || !proto.Equal(ref, a.Attempt) || r.worker != owner || !a.LeaseExpiresAt.AsTime().After(r.now) || !r.deadline.After(r.now) {
		return conflict("stale_lease")
	}
	return s.checkInputs(ctx, r.StoredRun, a)
}

// ClaimWork 在数据库锁内限制总并发并领取 Run；同领取键包含空结果也不会重复执行。
// ponytail: 首版最多两个租约，串行领取锁足够；吞吐提高时再拆分队列，不改变 Run 围栏。
func (s *Server) ClaimWork(ctx context.Context, q *pb.ClaimWorkRequest) (*pb.ClaimWorkResponse, error) {
	owner, err := workerIdentity(ctx, q.WorkerInstanceId)
	if err != nil {
		return nil, err
	}
	if !validID(q.ClaimRequestId) || len(q.Capabilities) == 0 || len(q.Capabilities) > 7 {
		return nil, invalid("invalid_claim")
	}
	kinds := []int32{}
	seen := map[pb.WorkKind]bool{}
	for _, k := range q.Capabilities {
		if k < 1 || k > 7 || seen[k] {
			return nil, invalid("invalid_capabilities")
		}
		seen[k] = true
		kinds = append(kinds, int32(k))
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(7185429003)"); err != nil {
		return nil, storage(err)
	}
	digest := messageHash(q)
	var previousRun, previousAttempt, previousHash string
	err = tx.QueryRow(ctx, `SELECT run_id,attempt_id,capabilities_hash FROM challenge.claims WHERE worker_id=$1 AND request_id=$2`, owner, q.ClaimRequestId).Scan(&previousRun, &previousAttempt, &previousHash)
	if err == nil {
		if previousHash != digest {
			return nil, conflict("claim_conflict")
		}
		if previousRun == "" {
			return &pb.ClaimWorkResponse{}, nil
		}
		r, e := load(ctx, tx, previousRun, true)
		if e != nil {
			return nil, e
		}
		if r.Assignment == nil || r.Assignment.Attempt.GetAttemptId() != previousAttempt {
			return nil, conflict("stale_claim")
		}
		if e = s.live(ctx, r, r.Assignment.Attempt); e != nil {
			return nil, e
		}
		return &pb.ClaimWorkResponse{Assignment: r.Assignment}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	var busy int
	var own bool
	if err = tx.QueryRow(ctx, `SELECT count(*),COALESCE(bool_or(worker_id=$1),false) FROM challenge.runs WHERE leased AND status='active'`, owner).Scan(&busy, &own); err != nil {
		return nil, storage(err)
	}
	out := &pb.ClaimWorkResponse{}
	runID, attemptID := "", ""
	if busy < 2 && !own {
		err = tx.QueryRow(ctx, `SELECT id FROM challenge.runs WHERE status='active' AND pending='' AND NOT leased AND kind=ANY($1) AND deadline>clock_timestamp() ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`, kinds).Scan(&runID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, storage(err)
		}
		if runID != "" {
			r, e := load(ctx, tx, runID, true)
			if e != nil {
				return nil, e
			}
			if e = s.checkInputs(ctx, r.StoredRun, r.Assignment); e != nil {
				if status.Code(e) == codes.Unavailable || status.Code(e) == codes.DeadlineExceeded {
					return nil, e
				}
				if status.Convert(e).Message() == "ranker_fit_invalidated" || status.Convert(e).Message() == "input_policy_changed" {
					r.Run.RankerFit.Status = "invalidated"
				}
				finish(r, "failed", "input_or_fit_invalidated")
				if e = save(ctx, tx, r); e != nil {
					return nil, e
				}
				runID = ""
			} else {
				r.Epoch++
				attemptID = id("attempt_")
				a := r.Assignment
				a.Attempt = &pb.AttemptRef{RunId: runID, WorkItemId: a.WorkItemId, AttemptId: attemptID, LeaseEpoch: r.Epoch, WorkerInstanceId: q.WorkerInstanceId}
				expires := r.now.Add(s.Config.Lease)
				if expires.After(r.deadline) {
					expires = r.deadline
				}
				a.LeaseExpiresAt = timestamppb.New(expires)
				a.Deadline = timestamppb.New(r.deadline)
				r.worker = owner
				r.GrantHash = ""
				r.ExecutionInstanceId = ""
				r.ProgressSequence = 0
				r.ProgressPhase = ""
				out.Assignment = a
				if e = save(ctx, tx, r); e != nil {
					return nil, e
				}
			}
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO challenge.claims(worker_id,request_id,capabilities_hash,run_id,attempt_id) VALUES($1,$2,$3,$4,$5)`, owner, q.ClaimRequestId, digest, runID, attemptID); err != nil {
		return nil, storage(err)
	}
	return out, storage(tx.Commit(ctx))
}
func (s *Server) RenewLease(ctx context.Context, q *pb.RenewLeaseRequest) (*pb.RenewLeaseResponse, error) {
	if q.Attempt == nil {
		return nil, invalid("attempt_required")
	}
	out := &pb.RenewLeaseResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ pgx.Tx, r *record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		expires := r.now.Add(s.Config.Lease)
		if expires.After(r.deadline) {
			expires = r.deadline
		}
		r.Assignment.LeaseExpiresAt = timestamppb.New(expires)
		out.ExpiresAt = r.Assignment.LeaseExpiresAt
		return nil
	})
	return out, err
}
func (s *Server) ReportProgress(ctx context.Context, q *pb.ReportProgressRequest) (*pb.ReportProgressResponse, error) {
	if q.Attempt == nil || q.Sequence < 1 || !validID(q.Phase) {
		return nil, invalid("invalid_progress")
	}
	out := &pb.ReportProgressResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ pgx.Tx, r *record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if q.Sequence < r.ProgressSequence || q.Sequence == r.ProgressSequence && q.Phase != r.ProgressPhase {
			return conflict("progress_conflict")
		}
		r.ProgressSequence = q.Sequence
		r.ProgressPhase = q.Phase
		out.AckSequence = q.Sequence
		return nil
	})
	return out, err
}
func (s *Server) ActivateAttempt(ctx context.Context, q *pb.ActivateAttemptRequest) (*pb.ActivateAttemptResponse, error) {
	if q.Attempt == nil || !validID(q.ExecutionInstanceId) {
		return nil, invalid("invalid_activation")
	}
	out := &pb.ActivateAttemptResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ pgx.Tx, r *record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if r.GrantHash != "" {
			return conflict("attempt_already_activated")
		}
		out.Grant = id("grant_")
		r.GrantHash = hash([]byte(out.Grant))
		r.ExecutionInstanceId = q.ExecutionInstanceId
		out.ExpiresAt = r.Assignment.Deadline
		return nil
	})
	return out, err
}
func grantOK(r *record, grant string) bool {
	return grant != "" && r.GrantHash != "" && subtle.ConstantTimeCompare([]byte(hash([]byte(grant))), []byte(r.GrantHash)) == 1
}
func (s *Server) CheckTaskAccess(ctx context.Context, q *pb.CheckTaskAccessRequest) (*pb.CheckTaskAccessResponse, error) {
	if q.Attempt == nil {
		return nil, invalid("attempt_required")
	}
	r, err := s.read(ctx, q.Attempt.RunId)
	if err != nil {
		return nil, err
	}
	if err = s.live(ctx, r, q.Attempt); err != nil {
		return nil, err
	}
	if !grantOK(r, q.Grant) {
		return nil, denied("invalid_grant")
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
		return nil, denied("resource_forbidden")
	}
	return &pb.CheckTaskAccessResponse{Allowed: true, ExpiresAt: r.Assignment.LeaseExpiresAt}, nil
}

// ReserveModelCall 事务内预留一个调用单位，只给第一次请求一次发送权。
func (s *Server) ReserveModelCall(ctx context.Context, q *pb.ReserveModelCallRequest) (*pb.ReserveModelCallResponse, error) {
	if q.Attempt == nil || !validID(q.RequestId) || len(q.RequestDigest) != 64 {
		return nil, invalid("invalid_model_call")
	}
	out := &pb.ReserveModelCallResponse{}
	err := s.mutate(ctx, q.Attempt.RunId, func(tx pgx.Tx, r *record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if !grantOK(r, q.Grant) {
			return denied("invalid_grant")
		}
		if q.Model == "" || q.Model != r.Assignment.Model.GetModel() && q.Model != r.Assignment.Executor.GetModel() {
			return denied("model_forbidden")
		}
		var previous string
		err := tx.QueryRow(ctx, `SELECT id,state,request_digest FROM challenge.model_calls WHERE attempt_id=$1 AND request_id=$2`, q.Attempt.AttemptId, q.RequestId).Scan(&out.ReservationId, &out.State, &previous)
		if err == nil {
			if previous != q.RequestDigest {
				return conflict("model_call_conflict")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return storage(err)
		}
		var count, unknown int
		if err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE state IN ('reserved','unknown')) FROM challenge.model_calls WHERE attempt_id=$1`, q.Attempt.AttemptId).Scan(&count, &unknown); err != nil {
			return storage(err)
		}
		if unknown > 0 {
			return conflict("model_outcome_unknown")
		}
		if count >= int(r.Assignment.ModelCallLimit) || r.ReservedCalls >= r.Run.Configuration.Budget.Amount {
			return status.Error(codes.ResourceExhausted, "model_budget_exhausted")
		}
		out.ReservationId = id("call_")
		out.State = "reserved"
		out.DispatchPermit = true
		_, err = tx.Exec(ctx, `INSERT INTO challenge.model_calls(id,run_id,attempt_id,request_id,request_digest,worker_id,lease_epoch,state) VALUES($1,$2,$3,$4,$5,$6,$7,'reserved')`, out.ReservationId, r.Run.Id, q.Attempt.AttemptId, q.RequestId, q.RequestDigest, r.worker, q.Attempt.LeaseEpoch)
		if err != nil {
			return storage(err)
		}
		r.ReservedCalls++
		return nil
	})
	return out, err
}

// SettleModelCall 终态仍可核对既有调用；从不重新发放发送权或释放未知预留。
func (s *Server) SettleModelCall(ctx context.Context, q *pb.SettleModelCallRequest) (*pb.SettleModelCallResponse, error) {
	if q.Attempt == nil || q.Outcome != "succeeded" && q.Outcome != "failed" && q.Outcome != "unknown" {
		return nil, invalid("invalid_settlement")
	}
	owner, err := workerIdentity(ctx, q.Attempt.WorkerInstanceId)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	var previous string
	err = tx.QueryRow(ctx, `SELECT state FROM challenge.model_calls WHERE id=$1 AND run_id=$2 AND attempt_id=$3 AND worker_id=$4 AND lease_epoch=$5 FOR UPDATE`, q.ReservationId, q.Attempt.RunId, q.Attempt.AttemptId, owner, q.Attempt.LeaseEpoch).Scan(&previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, denied("reservation_forbidden")
	}
	if err != nil {
		return nil, storage(err)
	}
	if previous != "reserved" && previous != q.Outcome {
		return nil, conflict("settlement_conflict")
	}
	_, err = tx.Exec(ctx, `UPDATE challenge.model_calls SET state=$2 WHERE id=$1`, q.ReservationId, q.Outcome)
	if err != nil {
		return nil, storage(err)
	}
	return &pb.SettleModelCallResponse{State: q.Outcome}, storage(tx.Commit(ctx))
}

// ResultDigest 只对类型化结果取摘要，执行标识另外按租约验证。
func ResultDigest(q *pb.CompleteWorkRequest) string {
	copy := proto.Clone(q).(*pb.CompleteWorkRequest)
	copy.Attempt = nil
	copy.ResultDigest = ""
	return messageHash(copy)
}

// CompleteWork 原子提交回执、当前提示与下一步；R 通过与 P/E 放行发生在同一事务。
func (s *Server) CompleteWork(ctx context.Context, q *pb.CompleteWorkRequest) (*pb.CompleteWorkResponse, error) {
	if q.Attempt == nil || q.Result == nil || q.ResultDigest != ResultDigest(q) {
		return nil, invalid("invalid_result_digest")
	}
	owner, err := workerIdentity(ctx, q.Attempt.WorkerInstanceId)
	if err != nil {
		return nil, err
	}
	err = s.mutate(ctx, q.Attempt.RunId, func(tx pgx.Tx, r *record) error {
		var previous, previousOwner, workID string
		var epoch int64
		err := tx.QueryRow(ctx, `SELECT result_digest,worker_id,lease_epoch,work_item_id FROM challenge.receipts WHERE attempt_id=$1 AND run_id=$2`, q.Attempt.AttemptId, q.Attempt.RunId).Scan(&previous, &previousOwner, &epoch, &workID)
		if err == nil {
			if previous != q.ResultDigest || previousOwner != owner || epoch != q.Attempt.LeaseEpoch || workID != q.Attempt.WorkItemId {
				return conflict("result_conflict")
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return storage(err)
		}
		if err = s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		if r.GrantHash == "" {
			return conflict("attempt_not_activated")
		}
		var unresolved int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM challenge.model_calls WHERE attempt_id=$1 AND state IN ('reserved','unknown')`, q.Attempt.AttemptId).Scan(&unresolved); err != nil {
			return storage(err)
		}
		if unresolved != 0 {
			return conflict("model_outcome_unknown")
		}
		a := r.Assignment
		switch a.Kind {
		case pb.WorkKind_WORK_KIND_VALIDATE_RANKER:
			score, e := FitScore(a.GetRank().Samples, r.Validation.Samples, q.GetRankings(), r.Run.Configuration.RankerMinComparablePairs)
			if e != nil {
				return e
			}
			r.Run.RankerFit = &pb.RankerFit{Status: "not_met", Score: &score, EvaluatedAt: timestamppb.New(r.now)}
			r.Assignment = nil
			if score > r.Run.Configuration.RankerFitThreshold {
				r.Run.RankerFit.Status = "passed"
				r.FitBinding = s.fitBinding(r.StoredRun)
				r.Validation.Samples = nil
				r.Training = nil
				r.TrainingInputs = nil
				r.TrainingRankings = nil
				s.nextPack(r.StoredRun)
			} else if r.Run.RankerRound >= r.Run.Configuration.RankerRoundLimit {
				finish(r, "failed", "ranker_fit_not_met")
			} else {
				r.Pending = "training"
			}
		case pb.WorkKind_WORK_KIND_TRAIN_RANKER:
			if err = validateRankings(a.GetRank().Samples, q.GetRankings()); err != nil {
				return err
			}
			input := &pb.RefineRankerInput{PreviousFit: r.Run.RankerFit.GetScore()}
			byTask := map[string]*pb.Ranking{}
			for _, v := range q.GetRankings().Items {
				byTask[v.TaskId] = v
			}
			for i, sample := range r.TrainingInputs {
				input.Samples = append(input.Samples, &pb.TrainingSample{Sample: sample, Ranking: byTask[sample.Task.TaskId], HumanCounts: r.Training.Samples[i].CountsByWork})
			}
			next := s.assignment(r.StoredRun, pb.WorkKind_WORK_KIND_REFINE_RANKER)
			next.Input = &pb.Assignment_Refine{Refine: input}
			next.Materials = rankMaterials(r.TrainingInputs)
			r.Assignment = next
		case pb.WorkKind_WORK_KIND_REFINE_RANKER:
			if r.Run.Stage != "optimizing_ranker" || !bounded(q.GetPrompt().GetPrompt(), 16384) {
				return invalid("invalid_ranker_prompt")
			}
			// 排序提示只保留通用规则，不允许把训练评论原文带入面向 P 的独立解释。
			for _, sample := range r.TrainingInputs {
				for _, c := range sample.Comments {
					if len(c.Body) >= 16 && strings.Contains(q.GetPrompt().Prompt, c.Body) {
						return invalid("training_material_in_prompt")
					}
				}
			}
			r.RankerPrompt = q.GetPrompt().Prompt
			r.Run.RankerRound++
			r.Pending = "validation"
			r.Assignment = nil
		case pb.WorkKind_WORK_KIND_PACK_TASK:
			if !bounded(q.GetPrompt().GetPrompt(), 16384) {
				return invalid("invalid_packer_prompt")
			}
			r.ExecutionHints = q.GetPrompt().Prompt
			next := s.assignment(r.StoredRun, pb.WorkKind_WORK_KIND_EXECUTE_PACKAGE)
			next.Input = &pb.Assignment_Execute{Execute: &pb.ExecuteInput{Initial: r.Run.Configuration.InitialTaskPackage, ExecutionHints: r.ExecutionHints}}
			next.Materials = selectedMaterials(r.StoredRun)
			r.Assignment = next
		case pb.WorkKind_WORK_KIND_EXECUTE_PACKAGE:
			g := q.GetGenerated()
			if g == nil || !validWork(g.Work) || g.Work.Id != "work_"+q.Attempt.AttemptId || !bounded(g.Report, 16384) {
				return invalid("invalid_generated_work")
			}
			for _, part := range g.Work.Artifacts {
				if f := part.GetFile(); f != nil {
					response, e := s.Asset.GetAsset(ctx, &asset.GetAssetRequest{AssetId: f.Id, Access: &asset.Access{TaskId: r.Run.TaskId, RunId: r.Run.Id, AttemptId: q.Attempt.AttemptId, Purpose: "output"}})
					if e != nil {
						return unavailable()
					}
					if !proto.Equal(response.Asset, f) || f.RunId != r.Run.Id || f.AttemptId != q.Attempt.AttemptId {
						return denied("artifact_not_owned")
					}
				}
			}
			r.Feedback = &pb.OwnFeedback{Work: g.Work, Report: g.Report, RankerFitScore: r.Run.RankerFit.GetScore(), EvaluationId: r.FitBinding}
			r.SourceAttemptId = q.Attempt.AttemptId
			r.Run.Candidates = append(r.Run.Candidates, &pb.Candidate{Id: g.Work.Id, RegistrationState: "unregistered"})
			r.Pending = "rank"
			r.Assignment = nil
		case pb.WorkKind_WORK_KIND_RANK_WORKS:
			if err = validateRankings(a.GetRank().Samples, q.GetRankings()); err != nil {
				return err
			}
			for i, id := range q.GetRankings().Items[0].OrderedWorkIds {
				if id == r.Feedback.Work.Id {
					r.Feedback.Rank = int32(i + 1)
				}
			}
			r.Feedback.PopulationSize = int32(len(q.GetRankings().Items[0].OrderedWorkIds))
			next := s.assignment(r.StoredRun, pb.WorkKind_WORK_KIND_EXPLAIN_OWN_WORK)
			next.Input = &pb.Assignment_Explain{Explain: &pb.ExplainInput{TaskDescription: r.Task.Description, OwnWork: r.Feedback.Work}}
			next.Materials = append(selectedMaterials(r.StoredRun), workMaterials(r.Run.TaskId, r.Feedback.Work, true)...)
			r.Assignment = next
		case pb.WorkKind_WORK_KIND_EXPLAIN_OWN_WORK:
			if err = validateJudgment(q.GetJudgment(), r.Feedback.Work, r.Run.TaskId); err != nil {
				return err
			}
			r.Feedback.Judgment = q.GetJudgment()
			r.Assignment = nil
			if r.Run.Round >= r.Run.Configuration.RoundLimit {
				r.Run.Stage = "registering"
				r.Pending = "register"
			} else {
				s.nextPack(r.StoredRun)
			}
		default:
			return invalid("invalid_work_kind")
		}
		r.GrantHash = ""
		r.ExecutionInstanceId = ""
		r.ProgressSequence = 0
		r.ProgressPhase = ""
		_, err = tx.Exec(ctx, `INSERT INTO challenge.receipts(attempt_id,run_id,worker_id,lease_epoch,work_item_id,result_digest) VALUES($1,$2,$3,$4,$5,$6)`, q.Attempt.AttemptId, r.Run.Id, owner, q.Attempt.LeaseEpoch, q.Attempt.WorkItemId, q.ResultDigest)
		return storage(err)
	})
	return &pb.CompleteWorkResponse{Accepted: err == nil}, err
}
func (s *Server) FailWork(ctx context.Context, q *pb.FailWorkRequest) (*pb.FailWorkResponse, error) {
	if q.Attempt == nil {
		return nil, invalid("attempt_required")
	}
	allowed := map[string]bool{"model_failed": true, "model_outcome_unknown": true, "invalid_model_result": true, "executor_failed": true, "executor_unavailable": true, "tool_limit_exceeded": true, "material_unavailable": true, "model_budget_exhausted": true}
	if !allowed[q.FailureCode] {
		return nil, invalid("invalid_failure_code")
	}
	err := s.mutate(ctx, q.Attempt.RunId, func(_ pgx.Tx, r *record) error {
		if err := s.live(ctx, r, q.Attempt); err != nil {
			return err
		}
		reason := q.FailureCode
		if !q.OutcomeKnown {
			reason = "attempt_outcome_unknown"
		}
		finish(r, "failed", reason)
		return nil
	})
	return &pb.FailWorkResponse{Accepted: err == nil}, err
}

func materialAccess(r *record, ref *pb.AttemptRef) *asset.Access {
	return &asset.Access{TaskId: r.Run.TaskId, RunId: r.Run.Id, AttemptId: ref.AttemptId, Purpose: fmt.Sprint(r.Assignment.Kind)}
}
