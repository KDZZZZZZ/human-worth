package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func (s *Server) GetEntryReviewSnapshot(ctx context.Context, r *pb.GetEntryReviewSnapshotRequest) (*pb.GetEntryReviewSnapshotResponse, error) {
	if err := requireContentCaller(ctx, "moderation"); err != nil {
		return nil, err
	}
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT snapshot FROM content.entry_review_requests WHERE id=$1`, r.GetReviewId()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "review_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	snap := &pb.EntryReviewSnapshot{}
	if protojson.Unmarshal(raw, snap) != nil {
		return nil, status.Error(codes.Internal, "invalid_entry_snapshot")
	}
	return &pb.GetEntryReviewSnapshotResponse{Snapshot: snap}, nil
}
func (s *Server) localApplyEntryReviewDecision(ctx context.Context, r *pb.ApplyEntryReviewDecisionRequest) (*pb.ApplyEntryReviewDecisionResponse, error) {
	if err := decisionCaller(ctx); err != nil {
		return nil, err
	}
	if err := validateApplyEntryReviewDecision(r); err != nil {
		return nil, err
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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7185429007))`, r.DecisionId); err != nil {
		return nil, storage(err)
	}
	var oldHash, receipt []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,response FROM content.entry_decision_receipts WHERE decision_id=$1`, r.DecisionId).Scan(&oldHash, &receipt)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
		}
		out := &pb.ApplyEntryReviewDecisionResponse{}
		if protojson.Unmarshal(receipt, out) != nil {
			return nil, status.Error(codes.Internal, "invalid_review_receipt")
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	// Global order: decision -> parent -> submission -> review. Takedown locks
	// the parent too. No RPC/storage call exists anywhere in this transaction.
	var parentState string
	var position int64
	err = tx.QueryRow(ctx, `SELECT state,next_entry_position FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE`, r.TaskId).Scan(&parentState, &position)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	sub, err := scanEntrySubmission(tx.QueryRow(ctx, `SELECT `+entrySubmissionColumns+` FROM content.entry_submissions s WHERE s.id=$1 AND s.task_id=$2 FOR UPDATE`, r.EntryId, r.TaskId))
	if err != nil {
		return nil, err
	}
	if sub.Revision != r.SubmissionRevision || sub.ReviewId != r.ReviewId || sub.State != "pending_review" {
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	var raw []byte
	var digest, reviewState string
	err = tx.QueryRow(ctx, `SELECT snapshot,snapshot_hash,state FROM content.entry_review_requests WHERE id=$1 AND entry_id=$2 AND submission_revision=$3 FOR UPDATE`, r.ReviewId, r.EntryId, r.SubmissionRevision).Scan(&raw, &digest, &reviewState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "review_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	if digest != r.SnapshotSha256 {
		return nil, status.Error(codes.AlreadyExists, "review_snapshot_conflict")
	}
	if reviewState != "pending_review" {
		return nil, status.Error(codes.Aborted, "review_already_decided")
	}
	snap := &pb.EntryReviewSnapshot{}
	if protojson.Unmarshal(raw, snap) != nil {
		return nil, status.Error(codes.Internal, "invalid_entry_snapshot")
	}
	frozen := snap.GetSubmission()
	if frozen.GetId() != r.EntryId || frozen.GetTaskId() != r.TaskId || frozen.GetAuthorId() != sub.AuthorId || frozen.GetRevision() != r.SubmissionRevision || frozen.GetReviewId() != r.ReviewId {
		return nil, status.Error(codes.Internal, "invalid_entry_snapshot")
	}
	out := &pb.ApplyEntryReviewDecisionResponse{DecisionId: r.DecisionId, ReviewId: r.ReviewId, EntryId: r.EntryId, Revision: r.SubmissionRevision, State: "rejected"}
	var reason *string
	var approved *time.Time
	if r.Outcome == pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
		// A blocked approval never republishes its parent. Rejection can still be
		// applied while the parent is down so the author receives its reason.
		if parentState != "published" {
			return nil, status.Error(codes.FailedPrecondition, "task_not_accepting_entries")
		}
		if position >= 2147483647 {
			return nil, status.Error(codes.FailedPrecondition, "entry_catalog_full")
		}
		data, e := validateEntry(frozen.Content)
		if e != nil {
			return nil, e
		}
		var now time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return nil, storage(err)
		}
		approved = &now
		_, err = tx.Exec(ctx, `INSERT INTO content.reviewed_entries(id,task_id,position,state,approved_at,origin,author_id,content) VALUES($1,$2,$3,'approved',$4,$7,$5,$6)`, r.EntryId, r.TaskId, position, now, frozen.AuthorId, data, frozen.Origin)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE content.reviewed_tasks SET next_entry_position=next_entry_position+1,catalog_revision=catalog_revision+1 WHERE task_id=$1`, r.TaskId)
		}
		out.State = "approved"
	} else {
		v := r.GetReason()
		reason = &v
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE content.entry_review_requests SET state=$2,reason=$3 WHERE id=$1`, r.ReviewId, out.State, reason)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE content.entry_submissions SET state=$2,rejection_reason=$3,approved_at=$4,updated_at=clock_timestamp() WHERE id=$1`, r.EntryId, out.State, reason, approved)
	}
	if err != nil {
		return nil, storage(err)
	}
	receipt, err = protojson.Marshal(out)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal_error")
	}
	_, err = tx.Exec(ctx, `INSERT INTO content.entry_decision_receipts(decision_id,review_id,request_hash,response) VALUES($1,$2,$3,$4)`, r.DecisionId, r.ReviewId, hash[:], receipt)
	if err != nil {
		return nil, storage(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return out, nil
}

// Same shared runner/lease mechanism as task cases, but a separate queue means
// a backlog of task requests cannot starve single-entry requests (or vice versa).
func (s *Server) deliverEntryReview(ctx context.Context, client moderationpb.ModerationServiceClient) (platform.OutboxResult, error) {
	result := platform.OutboxResult{Outcome: "idle", Stage: "claim"}
	if client == nil {
		return result, nil
	}
	token, err := reviewID("lease")
	if err != nil {
		return result, err
	}
	var id string
	err = s.DB.QueryRow(ctx, `UPDATE content.entry_review_outbox o SET lease_token=$1,available_at=clock_timestamp()+interval '15 seconds',attempts=attempts+1 FROM (SELECT id FROM content.entry_review_outbox WHERE delivered_at IS NULL AND NOT blocked AND available_at<=clock_timestamp() ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) q WHERE o.id=q.id RETURNING o.id`, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		result.Cause = err
		return result, storage(err)
	}
	result.HadWork = true
	result.ItemID = id
	result.Stage = "load_snapshot"
	var raw []byte
	err = s.DB.QueryRow(ctx, `SELECT snapshot FROM content.entry_review_requests WHERE id=$1`, id).Scan(&raw)
	snap := &pb.EntryReviewSnapshot{}
	if err == nil {
		err = protojson.Unmarshal(raw, snap)
	}
	if err != nil {
		result.Cause = err
		return result, storage(err)
	}
	result.Stage = "call_moderation"
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	out, err := client.CreateEntryReviewCase(call, &moderationpb.CreateEntryReviewCaseRequest{Snapshot: snap})
	cancel()
	blocked := false
	if err != nil {
		switch status.Code(err) {
		case codes.Aborted, codes.AlreadyExists, codes.InvalidArgument, codes.FailedPrecondition:
			blocked = true
			result.Reason = status.Convert(err).Message()
		default:
			result.Cause = err
			return result, err
		}
	}
	if !blocked && (out.GetReview().GetId() != id || !proto.Equal(out.GetReview().GetEntrySnapshot(), snap)) {
		err = status.Error(codes.Unavailable, "invalid_review_receipt")
		result.Cause = err
		return result, err
	}
	result.Stage = "confirm_delivery"
	tag, e := s.DB.Exec(ctx, `UPDATE content.entry_review_outbox SET delivered_at=CASE WHEN $3 THEN NULL ELSE clock_timestamp() END,blocked=$3,lease_token=NULL WHERE id=$1 AND lease_token=$2`, id, token, blocked)
	if e != nil {
		result.Cause = e
		return result, storage(e)
	}
	if tag.RowsAffected() == 0 {
		result.Outcome = "lease_lost"
		return result, nil
	}
	result.Outcome = "delivered"
	if blocked {
		result.Outcome = "blocked"
		result.Cause = err
	}
	return result, nil
}

// Used by real component tests and maintenance to progress one committed intent.
func (s *Server) DeliverEntryReview(ctx context.Context, client moderationpb.ModerationServiceClient) (bool, error) {
	r, e := s.deliverEntryReview(ctx, client)
	return r.HadWork, e
}

// Used to compare a request snapshot across services without JSON formatting.
func entrySnapshotDigest(snap *pb.EntryReviewSnapshot) string {
	wire, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(snap)
	h := sha256.Sum256(wire)
	return fmt.Sprintf("%x", h)
}

func validateApplyEntryReviewDecision(r *pb.ApplyEntryReviewDecisionRequest) error {
	if !keyPattern.MatchString(r.GetDecisionId()) || !taskPattern.MatchString(r.GetTaskId()) || !reviewedEntryPattern.MatchString(r.GetEntryId()) || r.GetSubmissionRevision() < 1 || len(r.GetSnapshotSha256()) != 64 {
		return invalid("invalid_review_decision")
	}
	if r.Outcome != pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE && r.Outcome != pb.ReviewOutcome_REVIEW_OUTCOME_REJECT {
		return invalid("invalid_review_decision")
	}
	if !utf8.ValidString(r.GetReason()) || strings.ContainsRune(r.GetReason(), 0) || len(r.GetReason()) > 4000 {
		return invalid("invalid_review_decision")
	}
	if r.Outcome == pb.ReviewOutcome_REVIEW_OUTCOME_REJECT && strings.TrimSpace(r.GetReason()) == "" {
		return invalid("rejection_reason_required")
	}
	return nil
}
