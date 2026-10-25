package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type catalogTxKey struct{}
type borrowedCatalogTx struct{ pgx.Tx }

func (b borrowedCatalogTx) Commit(context.Context) error   { return nil }
func (b borrowedCatalogTx) Rollback(context.Context) error { return nil }
func (s *Server) beginCatalogTx(ctx context.Context) (pgx.Tx, error) {
	if tx, ok := ctx.Value(catalogTxKey{}).(pgx.Tx); ok {
		return borrowedCatalogTx{tx}, nil
	}
	return s.DB.Begin(ctx)
}
func decisionCaller(ctx context.Context) error {
	if ctx.Value(catalogTxKey{}) != nil {
		return nil
	}
	return requireContentCaller(ctx, "moderation")
}
func (s *Server) localCatalog(ctx context.Context, method string, request proto.Message) (proto.Message, error) {
	switch method {
	case "task":
		return s.localApplyReviewDecision(ctx, request.(*pb.ApplyReviewDecisionRequest))
	case "entry":
		return s.localApplyEntryReviewDecision(ctx, request.(*pb.ApplyEntryReviewDecisionRequest))
	case "legacy":
		return s.localApplyAssetReview(ctx, request.(*pb.ApplyAssetReviewRequest))
	case "takedown":
		return s.localApplyTakedown(ctx, request.(*pb.ApplyTakedownRequest))
	case "bootstrap":
		return &pb.ApplyAssetReviewResponse{}, nil
	}
	return nil, status.Error(codes.Internal, "invalid_catalog_method")
}
func catalogMessage(method string) proto.Message {
	switch method {
	case "takedown":
		return &pb.ApplyTakedownRequest{}
	case "task":
		return &pb.ApplyReviewDecisionRequest{}
	case "entry":
		return &pb.ApplyEntryReviewDecisionRequest{}
	default:
		return &pb.ApplyAssetReviewRequest{}
	}
}
func catalogResult(method string) proto.Message {
	switch method {
	case "takedown":
		return &pb.ApplyTakedownResponse{}
	case "task":
		return &pb.ApplyReviewDecisionResponse{}
	case "entry":
		return &pb.ApplyEntryReviewDecisionResponse{}
	default:
		return &pb.ApplyAssetReviewResponse{}
	}
}
func (s *Server) ApplyReviewDecision(ctx context.Context, r *pb.ApplyReviewDecisionRequest) (*pb.ApplyReviewDecisionResponse, error) {
	if err := decisionCaller(ctx); err != nil {
		return nil, err
	}
	if err := validateApplyReviewDecision(r); err != nil {
		return nil, err
	}

	if r.GetOutcome() != pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
		return s.localApplyReviewDecision(ctx, r)
	}
	out, err := s.changeCatalog(ctx, "task", r.GetTaskId(), r)
	if err != nil {
		return nil, err
	}
	return out.(*pb.ApplyReviewDecisionResponse), nil
}
func (s *Server) ApplyEntryReviewDecision(ctx context.Context, r *pb.ApplyEntryReviewDecisionRequest) (*pb.ApplyEntryReviewDecisionResponse, error) {
	if err := decisionCaller(ctx); err != nil {
		return nil, err
	}
	if err := validateApplyEntryReviewDecision(r); err != nil {
		return nil, err
	}

	if r.GetOutcome() != pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
		return s.localApplyEntryReviewDecision(ctx, r)
	}
	out, err := s.changeCatalog(ctx, "entry", r.GetTaskId(), r)
	if err != nil {
		return nil, err
	}
	return out.(*pb.ApplyEntryReviewDecisionResponse), nil
}
func (s *Server) ApplyAssetReview(ctx context.Context, r *pb.ApplyAssetReviewRequest) (*pb.ApplyAssetReviewResponse, error) {
	if err := decisionCaller(ctx); err != nil {
		return nil, err
	}
	out, err := s.changeCatalog(ctx, "legacy", r.GetTaskId(), r)
	if err != nil {
		return nil, err
	}
	return out.(*pb.ApplyAssetReviewResponse), nil
}
func (s *Server) changeCatalog(ctx context.Context, method, task string, request proto.Message) (proto.Message, error) {
	if !taskPattern.MatchString(task) {
		return nil, invalid("invalid_review_decision")
	}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		return nil, invalid("invalid_review_decision")
	}
	hash := sha256.Sum256(wire)
	var decision, receiptTable string
	switch r := request.(type) {
	case *pb.ApplyTakedownRequest:
		decision = r.DecisionId
	case *pb.ApplyReviewDecisionRequest:
		decision = r.DecisionId
		receiptTable = "content.review_decision_receipts"
	case *pb.ApplyEntryReviewDecisionRequest:
		decision = r.DecisionId
		receiptTable = "content.entry_decision_receipts"
	case *pb.ApplyAssetReviewRequest:
		decision = r.DecisionId
		receiptTable = "content.asset_review_receipts"
	}
	stable := sha256.Sum256([]byte(method + ":" + decision))
	if method == "bootstrap" {
		stable = sha256.Sum256([]byte("bootstrap:" + task))
	}
	change := fmt.Sprintf("chg_%x", stable[:16])
	if method != "bootstrap" && receiptTable != "" {
		// Immutable completed receipts need no new intent, head lock or Voting RPC,
		// even during another change. An applied local receipt is NOT completion:
		// any existing catalog intent suppresses the pre-Voting fallback.
		var old, receipt []byte
		var local bool
		err = s.DB.QueryRow(ctx, `SELECT request_hash,convert_to(response::text,'UTF8'),true FROM `+receiptTable+` WHERE decision_id=$1 AND NOT EXISTS(SELECT 1 FROM content.voting_catalog_changes WHERE id=$2)
 UNION ALL SELECT request_hash,result,false FROM content.voting_catalog_changes WHERE id=$2 AND stage='completed'`, decision, change).Scan(&old, &receipt, &local)
		if err == nil {
			if !bytes.Equal(old, hash[:]) {
				return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
			}
			out := catalogResult(method)
			if local {
				err = protojson.Unmarshal(receipt, out)
			} else {
				err = proto.Unmarshal(receipt, out)
			}
			if err != nil {
				return nil, status.Error(codes.Internal, "invalid_catalog_receipt")
			}
			return out, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, storage(err)
		}
	}
	if s.Voting == nil {
		var initialized bool
		if err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content.voting_catalog_heads WHERE task_id=$1)`, task).Scan(&initialized); err != nil {
			return nil, storage(err)
		}
		if initialized {
			return nil, status.Error(codes.Unavailable, "voting_unavailable")
		}
		return s.localCatalog(ctx, method, request)
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO content.voting_catalog_heads(task_id) SELECT id FROM content.task_drafts WHERE id=$1 ON CONFLICT DO NOTHING`, task)
	if err != nil {
		return nil, storage(err)
	}
	var generation int64
	var active *string
	err = tx.QueryRow(ctx, `SELECT generation,active_change FROM content.voting_catalog_heads WHERE task_id=$1 FOR UPDATE`, task).Scan(&generation, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	var old []byte
	var stage string
	var result []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,stage,result FROM content.voting_catalog_changes WHERE id=$1`, change).Scan(&old, &stage, &result)
	if err == nil {
		if !bytes.Equal(old, hash[:]) {
			return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
		}
		if stage == "completed" {
			out := catalogResult(method)
			if proto.Unmarshal(result, out) != nil {
				return nil, status.Error(codes.Internal, "invalid_catalog_receipt")
			}
			return out, nil
		}
		if stage == "blocked" {
			return nil, status.Error(codes.FailedPrecondition, "catalog_change_blocked")
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	} else {
		if active != nil {
			return nil, status.Error(codes.Unavailable, "catalog_change_pending")
		}
		// Validate exact business command and complete target in a savepoint. No RPC.
		simulation, err := tx.Begin(ctx)
		if err != nil {
			return nil, storage(err)
		}
		_, err = s.localCatalog(context.WithValue(ctx, catalogTxKey{}, simulation), method, request)
		if err == nil {
			_, err = catalogSnapshot(ctx, simulation, task, generation+1)
		}
		if rollback := simulation.Rollback(ctx); rollback != nil {
			return nil, storage(rollback)
		}
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO content.voting_catalog_changes(id,task_id,generation,method,request,request_hash) VALUES($1,$2,$3,$4,$5,$6)`, change, task, generation+1, method, wire, hash[:])
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE content.voting_catalog_heads SET active_change=$2 WHERE task_id=$1`, task, change)
		}
		if err != nil {
			return nil, storage(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	// Try once synchronously; worker recovers the same durable intent after outage.
	_, err = s.deliverCatalogID(ctx, change)
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = s.DB.QueryRow(ctx, `SELECT result FROM content.voting_catalog_changes WHERE id=$1 AND stage='completed'`, change).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.Unavailable, "catalog_change_pending")
	}
	if err != nil {
		return nil, storage(err)
	}
	out := catalogResult(method)
	if proto.Unmarshal(raw, out) != nil {
		return nil, status.Error(codes.Internal, "invalid_catalog_receipt")
	}
	return out, nil
}
func (s *Server) deliverCatalogID(ctx context.Context, id string) (platform.OutboxResult, error) {
	result := platform.OutboxResult{ItemID: id, Stage: "claim", Outcome: "idle"}
	token, err := reviewID("lease")
	if err != nil {
		return result, err
	}
	var task, method, stage string
	var generation int64
	var request, catalog []byte
	err = s.DB.QueryRow(ctx, `UPDATE content.voting_catalog_changes SET lease_token=$2,available_at=clock_timestamp()+interval '15 seconds',attempts=attempts+1 WHERE id=$1 AND stage IN ('queued','applied') AND available_at<=clock_timestamp() RETURNING task_id,method,stage,generation,request,catalog`, id, token).Scan(&task, &method, &stage, &generation, &request, &catalog)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, storage(err)
	}
	result.HadWork = true
	result.Stage = "prepare_voting"
	prepared, err := s.Voting.PrepareCatalogChange(ctx, &votingpb.PrepareCatalogChangeRequest{ChangeId: id, TaskId: task, ExpectedGeneration: generation - 1, Generation: generation})
	if err != nil {
		return s.catalogDeliveryFailure(ctx, result, id, token, err)
	}
	if prepared.GetTransition().GetChangeId() != id || prepared.GetTransition().GetGeneration() != generation {
		return result, status.Error(codes.Unavailable, "invalid_catalog_receipt")
	}
	if stage == "queued" {
		result.Stage = "apply_content"
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return result, storage(err)
		}
		defer tx.Rollback(ctx)
		var active *string
		var currentToken *string
		if err = tx.QueryRow(ctx, `SELECT active_change FROM content.voting_catalog_heads WHERE task_id=$1 FOR UPDATE`, task).Scan(&active); err != nil {
			return result, storage(err)
		}
		if err = tx.QueryRow(ctx, `SELECT stage,lease_token,catalog FROM content.voting_catalog_changes WHERE id=$1 FOR UPDATE`, id).Scan(&stage, &currentToken, &catalog); err != nil {
			return result, storage(err)
		}
		if active == nil || *active != id || currentToken == nil || *currentToken != token {
			result.Outcome = "lease_lost"
			return result, nil
		}
		if stage == "queued" {
			r := catalogMessage(method)
			if proto.Unmarshal(request, r) != nil {
				return result, status.Error(codes.Internal, "invalid_catalog_intent")
			}
			out, e := s.localCatalog(context.WithValue(ctx, catalogTxKey{}, tx), method, r)
			if e != nil {
				tx.Rollback(ctx)
				switch status.Code(e) {
				case codes.Aborted, codes.AlreadyExists, codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument:
					_, mark := s.DB.Exec(ctx, `UPDATE content.voting_catalog_changes SET stage='blocked',lease_token=NULL WHERE id=$1 AND lease_token=$2`, id, token)
					if mark != nil {
						return result, storage(mark)
					}
					result.Outcome = "blocked"
					result.Reason = status.Convert(e).Message()
					return result, nil
				}
				return result, e
			}
			c, e := catalogSnapshot(ctx, tx, task, generation)
			if e != nil {
				return result, e
			}
			catalog, _ = proto.Marshal(c)
			resultRaw, _ := proto.Marshal(out)
			_, err = tx.Exec(ctx, `UPDATE content.voting_catalog_changes SET stage='applied',result=$2,catalog=$3 WHERE id=$1 AND lease_token=$4`, id, resultRaw, catalog, token)
			if err != nil {
				return result, storage(err)
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return result, storage(err)
		}
	}
	result.Stage = "activate_voting"
	c := &pb.VotingCatalogSnapshot{}
	if proto.Unmarshal(catalog, c) != nil {
		return result, status.Error(codes.Internal, "invalid_catalog_intent")
	}
	activated, err := s.Voting.ActivateCatalogSnapshot(ctx, &votingpb.ActivateCatalogSnapshotRequest{ChangeId: id, Catalog: c})
	if err != nil {
		return s.catalogDeliveryFailure(ctx, result, id, token, err)
	}
	if activated.GetTransition().GetSha256() != c.Sha256 || activated.GetTransition().GetPhase() != "activated" {
		return result, status.Error(codes.Unavailable, "invalid_catalog_receipt")
	}
	result.Stage = "confirm_content"
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return result, storage(err)
	}
	defer tx.Rollback(ctx)
	var active *string
	if err = tx.QueryRow(ctx, `SELECT active_change FROM content.voting_catalog_heads WHERE task_id=$1 FOR UPDATE`, task).Scan(&active); err != nil {
		return result, storage(err)
	}
	tag, err := tx.Exec(ctx, `UPDATE content.voting_catalog_changes SET stage='completed',completed_at=clock_timestamp(),lease_token=NULL WHERE id=$1 AND stage='applied' AND lease_token=$2`, id, token)
	if err != nil {
		return result, storage(err)
	}
	if tag.RowsAffected() == 0 {
		result.Outcome = "lease_lost"
		return result, nil
	}
	if active == nil || *active != id {
		return result, status.Error(codes.Internal, "invalid_catalog_head")
	}
	_, err = tx.Exec(ctx, `UPDATE content.voting_catalog_heads SET generation=$2,active_change=NULL WHERE task_id=$1 AND active_change=$3`, task, generation, id)
	if err != nil {
		return result, storage(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return result, storage(err)
	}
	result.Outcome = "delivered"
	return result, nil
}
func (s *Server) DeliverCatalog(ctx context.Context) (platform.OutboxResult, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM content.voting_catalog_changes WHERE stage IN ('queued','applied') AND available_at<=clock_timestamp() ORDER BY available_at,id LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return platform.OutboxResult{Outcome: "idle"}, nil
	}
	if err != nil {
		return platform.OutboxResult{}, storage(err)
	}
	return s.deliverCatalogID(ctx, id)
}
func (s *Server) RunCatalogDelivery(ctx context.Context, logger *slog.Logger) {
	platform.RunOutbox(ctx, logger, "content_to_voting", s.DeliverCatalog)
}
func (s *Server) BootstrapCatalogs(ctx context.Context) error {
	if s.Voting == nil {
		return status.Error(codes.Unavailable, "voting_unavailable")
	}
	for {
		rows, err := s.DB.Query(ctx, `SELECT t.task_id FROM content.reviewed_tasks t LEFT JOIN content.voting_catalog_heads h ON h.task_id=t.task_id WHERE h.task_id IS NULL OR h.generation=0 OR h.active_change IS NOT NULL ORDER BY t.task_id LIMIT 256`)
		if err != nil {
			return storage(err)
		}
		var tasks []string
		for rows.Next() {
			var task string
			if err = rows.Scan(&task); err != nil {
				rows.Close()
				return storage(err)
			}
			tasks = append(tasks, task)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return storage(err)
		}
		if len(tasks) == 0 {
			return nil
		}
		for _, task := range tasks {
			var active *string
			if err = s.DB.QueryRow(ctx, `SELECT active_change FROM content.voting_catalog_heads WHERE task_id=$1`, task).Scan(&active); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return storage(err)
			}
			if active != nil {
				var stage string
				if err = s.DB.QueryRow(ctx, `SELECT stage FROM content.voting_catalog_changes WHERE id=$1`, *active).Scan(&stage); err != nil {
					return storage(err)
				}
				if stage == "blocked" {
					return status.Error(codes.FailedPrecondition, "catalog_change_blocked")
				}
				if _, err = s.deliverCatalogID(ctx, *active); err != nil {
					return err
				}
				var remaining *string
				if err = s.DB.QueryRow(ctx, `SELECT active_change FROM content.voting_catalog_heads WHERE task_id=$1`, task).Scan(&remaining); err != nil {
					return storage(err)
				}
				if remaining != nil {
					return status.Error(codes.Unavailable, "catalog_change_pending")
				}
				continue
			}
			if _, err = s.changeCatalog(ctx, "bootstrap", task, &pb.ApplyAssetReviewRequest{}); err != nil {
				return err
			}
		}
	}
}

// Terminal protocol conflicts remain visible to operators, not an endless retry.
// The active head and remote frozen gate are deliberately NOT cleared on error.
func (s *Server) catalogDeliveryFailure(ctx context.Context, result platform.OutboxResult, id, token string, cause error) (platform.OutboxResult, error) {
	// Runtime admission/transport exhaustion is retryable. Only the explicit
	// catalog validation reasons establish a permanent ResourceExhausted failure.
	if status.Code(cause) == codes.ResourceExhausted && status.Convert(cause).Message() != "catalog_too_large_or_invalid" && status.Convert(cause).Message() != "catalog_too_large" {
		return result, cause
	}
	switch status.Code(cause) {
	case codes.Aborted, codes.AlreadyExists, codes.FailedPrecondition, codes.NotFound, codes.InvalidArgument, codes.ResourceExhausted:
		_, err := s.DB.Exec(ctx, `UPDATE content.voting_catalog_changes SET stage='blocked',lease_token=NULL WHERE id=$1 AND lease_token=$2 AND stage IN ('queued','applied')`, id, token)
		if err != nil {
			return result, storage(err)
		}
		result.Outcome, result.Reason = "blocked", status.Convert(cause).Message()
		return result, nil
	}
	return result, cause
}
