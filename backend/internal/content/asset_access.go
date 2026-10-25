package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"regexp"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var reviewedEntryPattern = regexp.MustCompile(`^ent_[a-f0-9]{32}$`)
var reviewedAssetPattern = regexp.MustCompile(`^ast_[a-f0-9]{32}$`)

func requireContentCaller(ctx context.Context, expected string) error {
	name, err := platform.ServiceName(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if name != expected {
		return status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return nil
}

// Reads only Content's committed authority. Never call Asset from this method.
func (s *Server) CheckAssetAccess(ctx context.Context, r *pb.CheckAssetAccessRequest) (*pb.CheckAssetAccessResponse, error) {
	if err := requireContentCaller(ctx, "asset"); err != nil {
		return nil, err
	}
	ref := r.GetReference()
	result := &pb.CheckAssetAccessResponse{}
	if taskPattern.MatchString(ref.GetTaskId()) && ref.GetEntryId() == "" && reviewedAssetPattern.MatchString(ref.GetAssetId()) {
		err := s.DB.QueryRow(ctx, `SELECT revision,state='published' AND `+catalogPublic+` AND COALESCE(snapshot->'taskAttachments','[]'::jsonb) @> jsonb_build_array(jsonb_build_object('assetId',$2::text)) FROM content.reviewed_tasks t WHERE task_id=$1`, ref.TaskId, ref.AssetId).Scan(&result.Revision, &result.Allowed)
		if errors.Is(err, pgx.ErrNoRows) {
			return result, nil
		}
		if err != nil {
			return nil, storage(err)
		}
		return result, nil
	}
	if !taskPattern.MatchString(ref.GetTaskId()) || !reviewedEntryPattern.MatchString(ref.GetEntryId()) || !reviewedAssetPattern.MatchString(ref.GetAssetId()) {
		return result, nil
	}
	err := s.DB.QueryRow(ctx, `SELECT t.revision,
 t.state='published' AND `+catalogPublic+` AND e.state='approved' AND
 (`+publicEntryContent+`->'artifacts') @> jsonb_build_array(jsonb_build_object('kind','file','assetId',$3::text))
 FROM content.reviewed_tasks t JOIN content.reviewed_entries e ON e.task_id=t.task_id
 WHERE t.task_id=$1 AND e.id=$2`, ref.TaskId, ref.EntryId, ref.AssetId).Scan(&result.Revision, &result.Allowed)
	if errors.Is(err, pgx.ErrNoRows) {
		return &pb.CheckAssetAccessResponse{}, nil
	}
	if err != nil {
		return nil, storage(err)
	}
	return result, nil
}

// Applies an already-made trusted review decision, not a review workflow. No
// user-supplied asset IDs enter this method: all references come from the exact
// draft revision whose ownership was validated when it was written.
func (s *Server) localApplyAssetReview(ctx context.Context, r *pb.ApplyAssetReviewRequest) (*pb.ApplyAssetReviewResponse, error) {
	if err := decisionCaller(ctx); err != nil {
		return nil, err
	}
	if !keyPattern.MatchString(r.GetDecisionId()) || !taskPattern.MatchString(r.GetTaskId()) || r.GetSubmissionRevision() < 1 || r.GetExpectedRevision() < 0 {
		return nil, invalid("invalid_review_decision")
	}
	taskState := ""
	entryState := ""
	switch d := r.GetDecision().(type) {
	case *pb.ApplyAssetReviewRequest_TaskState:
		switch d.TaskState {
		case pb.ReviewedTaskState_REVIEWED_TASK_STATE_PUBLISHED:
			taskState = "published"
		case pb.ReviewedTaskState_REVIEWED_TASK_STATE_TAKEN_DOWN:
			taskState = "taken_down"
		default:
			return nil, invalid("invalid_review_decision")
		}
	case *pb.ApplyAssetReviewRequest_EntryReview:
		if !reviewedEntryPattern.MatchString(d.EntryReview.GetEntryId()) {
			return nil, invalid("invalid_review_decision")
		}
		switch d.EntryReview.GetState() {
		case pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVED:
			entryState = "approved"
		case pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVAL_REVOKED:
			entryState = "approval_revoked"
		default:
			return nil, invalid("invalid_review_decision")
		}
	default:
		return nil, invalid("invalid_review_decision")
	}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(r)
	if err != nil {
		return nil, invalid("invalid_review_decision")
	}
	hash := sha256.Sum256(wire)
	tx, err := s.beginCatalogTx(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	// Serialize global decision IDs as well as per-task state, including collisions
	// across tasks. Always acquire the decision lock before the task row lock.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7185429003))`, r.DecisionId); err != nil {
		return nil, storage(err)
	}
	// All decisions for a task and private draft edits lock the same row first.
	var draftRevision int64
	err = tx.QueryRow(ctx, `SELECT revision FROM content.task_drafts WHERE id=$1 FOR UPDATE`, r.TaskId).Scan(&draftRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	var oldHash, receipt []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,response FROM content.asset_review_receipts WHERE decision_id=$1`, r.DecisionId).Scan(&oldHash, &receipt)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
		}
		result := &pb.ApplyAssetReviewResponse{}
		if err = protojson.Unmarshal(receipt, result); err != nil {
			return nil, status.Error(codes.Internal, "invalid_review_receipt")
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	// Formal submissions cannot bypass the case/decision protocol. Existing
	// receipts above remain replayable; tightening existing visibility is allowed.
	if taskState == "published" || entryState == "approved" {
		var formal bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content.review_requests WHERE task_id=$1) OR EXISTS(SELECT 1 FROM content.entry_submissions WHERE task_id=$1 AND id=$2)`, r.TaskId, r.GetEntryReview().GetEntryId()).Scan(&formal); err != nil {
			return nil, storage(err)
		}
		if formal {
			return nil, status.Error(codes.FailedPrecondition, "formal_review_required")
		}
	}
	var revision, sourceRevision int64
	var currentState string
	err = tx.QueryRow(ctx, `SELECT revision,source_revision,state FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE`, r.TaskId).Scan(&revision, &sourceRevision, &currentState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "formal_review_required")
	} else {
		if err != nil {
			return nil, storage(err)
		}
		if s.Voting != nil && currentState == "taken_down" && taskState == "published" {
			return nil, status.Error(codes.FailedPrecondition, "task_permanently_taken_down")
		}
		if revision != r.ExpectedRevision || sourceRevision != r.SubmissionRevision {
			return nil, status.Error(codes.Aborted, "revision_conflict")
		}
		if entryState != "" {
			tag, e := tx.Exec(ctx, `UPDATE content.reviewed_entries SET state=$3 WHERE task_id=$1 AND id=$2`, r.TaskId, r.GetEntryReview().EntryId, entryState)
			if e != nil {
				return nil, storage(e)
			}
			if tag.RowsAffected() != 1 {
				return nil, status.Error(codes.NotFound, "entry_not_found")
			}
			if entryState == "approval_revoked" {
				if _, e = tx.Exec(ctx, `UPDATE content.entry_submissions SET state='approval_revoked',updated_at=clock_timestamp() WHERE id=$1 AND task_id=$2 AND state='approved'`, r.GetEntryReview().EntryId, r.TaskId); e != nil {
					return nil, storage(e)
				}
			}
			_, err = tx.Exec(ctx, `UPDATE content.reviewed_tasks SET revision=revision+1,catalog_revision=catalog_revision+1,updated_at=clock_timestamp() WHERE task_id=$1`, r.TaskId)
		} else {
			_, err = tx.Exec(ctx, `UPDATE content.reviewed_tasks SET state=$2,revision=revision+1,catalog_revision=catalog_revision+1,updated_at=clock_timestamp() WHERE task_id=$1`, r.TaskId, taskState)
		}
		if err != nil {
			return nil, storage(err)
		}
		revision++
	}
	if taskState == "taken_down" {
		_, err = tx.Exec(ctx, `UPDATE content.task_drafts SET state='taken_down',updated_at=clock_timestamp() WHERE id=$1 AND review_id IS NOT NULL`, r.TaskId)
		if err != nil {
			return nil, storage(err)
		}
	}
	result := &pb.ApplyAssetReviewResponse{Revision: revision}
	rows, err := tx.Query(ctx, `SELECT id,position,state FROM content.reviewed_entries WHERE task_id=$1 ORDER BY position`, r.TaskId)
	if err != nil {
		return nil, storage(err)
	}
	for rows.Next() {
		e := &pb.ReviewedEntry{}
		var state string
		if err = rows.Scan(&e.Id, &e.Position, &state); err != nil {
			rows.Close()
			return nil, storage(err)
		}
		e.State = pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVED
		if state == "approval_revoked" {
			e.State = pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVAL_REVOKED
		}
		result.Entries = append(result.Entries, e)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storage(err)
	}
	receipt, err = protojson.Marshal(result)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal_error")
	}
	_, err = tx.Exec(ctx, `INSERT INTO content.asset_review_receipts(decision_id,task_id,request_hash,response) VALUES($1,$2,$3,$4)`, r.DecisionId, r.TaskId, hash[:], receipt)
	if err != nil {
		return nil, storage(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return result, nil
}
