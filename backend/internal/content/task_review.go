package content

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func reviewID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", status.Error(codes.Internal, "internal_error")
	}
	return fmt.Sprintf("%s_%x", prefix, b), nil
}
func bundleRequired(input *pb.TaskDraftInput) error {
	human, agent := false, false
	for _, e := range input.GetEntries() {
		if err := workBodyRequired(e); err != nil {
			return err
		}
		human = human || e.GetSide() == "human"
		agent = agent || e.GetSide() == "agent"
	}
	if !human || !agent {
		return status.Error(codes.FailedPrecondition, "initial_bundle_required")
	}
	return nil
}
func (s *Server) submitted(ctx context.Context, task, author string, revision int64) (*pb.TaskSubmission, error) {
	var raw []byte
	var state string
	var reason *string
	err := s.DB.QueryRow(ctx, `SELECT snapshot,state,reason FROM content.review_requests WHERE task_id=$1 AND author_id=$2 AND submission_revision=$3`, task, author, revision).Scan(&raw, &state, &reason)
	if err != nil {
		return nil, err
	}
	snap := &pb.ReviewSnapshot{}
	if protojson.Unmarshal(raw, snap) != nil {
		return nil, status.Error(codes.Internal, "invalid_review_snapshot")
	}
	snap.Submission.State = state
	snap.Submission.RejectionReason = reason
	return snap.Submission, nil
}
func (s *Server) RequestTaskReview(ctx context.Context, r *pb.RequestTaskReviewRequest) (*pb.RequestTaskReviewResponse, error) {
	author, err := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_RequestTaskReview_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !taskPattern.MatchString(r.GetTaskId()) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if r.ExpectedRevision < 1 {
		return nil, invalid("invalid_revision")
	}
	if old, e := s.submitted(ctx, r.TaskId, author, r.ExpectedRevision); e == nil {
		return &pb.RequestTaskReviewResponse{Submission: old}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, storage(e)
	}
	before, _, err := scanTaskSubmission(s.DB.QueryRow(ctx, `SELECT `+taskSubmissionColumns+` FROM content.task_drafts WHERE id=$1 AND author_id=$2`, r.TaskId, author))
	if err != nil {
		return nil, err
	}
	if before.Revision != r.ExpectedRevision || before.State != "draft" {
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	if _, err = validate(before.Content); err != nil {
		return nil, err
	}
	if err = bundleRequired(before.Content); err != nil {
		return nil, err
	}
	if err = s.validateAssets(ctx, author, before.Content, r.AssetActorAssertion); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	current, _, err := scanTaskSubmission(tx.QueryRow(ctx, `SELECT `+taskSubmissionColumns+` FROM content.task_drafts WHERE id=$1 AND author_id=$2 FOR UPDATE`, r.TaskId, author))
	if err != nil {
		return nil, err
	}
	// A concurrent identical submission may have committed while Asset was checked.
	var existing []byte
	var existingState string
	var existingReason *string
	err = tx.QueryRow(ctx, `SELECT snapshot,state,reason FROM content.review_requests WHERE task_id=$1 AND submission_revision=$2`, r.TaskId, r.ExpectedRevision).Scan(&existing, &existingState, &existingReason)
	if err == nil {
		snap := &pb.ReviewSnapshot{}
		if protojson.Unmarshal(existing, snap) != nil {
			return nil, status.Error(codes.Internal, "invalid_review_snapshot")
		}
		snap.Submission.State = existingState
		snap.Submission.RejectionReason = existingReason
		return &pb.RequestTaskReviewResponse{Submission: snap.Submission}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	if current.Revision != before.Revision || current.State != "draft" {
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	// Existing 3C approved/taken-down snapshots cannot be republished as new drafts.
	var legacy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content.reviewed_tasks WHERE task_id=$1)`, r.TaskId).Scan(&legacy); err != nil {
		return nil, storage(err)
	}
	if legacy {
		return nil, status.Error(codes.FailedPrecondition, "already_reviewed")
	}
	id, err := reviewID("rev")
	if err != nil {
		return nil, err
	}
	var created time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&created); err != nil {
		return nil, storage(err)
	}
	current.State = "pending_review"
	current.ReviewId = &id
	snap := &pb.ReviewSnapshot{ReviewId: id, Submission: current, CreatedAt: timestamppb.New(created)}
	for range current.Content.Entries {
		entry, err := reviewID("ent")
		if err != nil {
			return nil, err
		}
		snap.EntryIds = append(snap.EntryIds, entry)
	}
	raw, err := protojson.Marshal(snap)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal_error")
	}
	_, err = tx.Exec(ctx, `INSERT INTO content.review_requests(id,task_id,author_id,submission_revision,snapshot,state,created_at) VALUES($1,$2,$3,$4,$5,'pending_review',$6);`, id, r.TaskId, author, r.ExpectedRevision, raw, created)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE content.task_drafts SET state='pending_review',review_id=$2,updated_at=clock_timestamp() WHERE id=$1`, r.TaskId, id)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO content.review_outbox(id) VALUES($1)`, id)
	}
	if err != nil {
		return nil, storage(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.RequestTaskReviewResponse{Submission: current}, nil
}
func (s *Server) GetReviewSnapshot(ctx context.Context, r *pb.GetReviewSnapshotRequest) (*pb.GetReviewSnapshotResponse, error) {
	if err := requireContentCaller(ctx, "moderation"); err != nil {
		return nil, err
	}
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT snapshot FROM content.review_requests WHERE id=$1`, r.GetReviewId()).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "review_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	snap := &pb.ReviewSnapshot{}
	if protojson.Unmarshal(raw, snap) != nil {
		return nil, status.Error(codes.Internal, "invalid_review_snapshot")
	}
	return &pb.GetReviewSnapshotResponse{Snapshot: snap}, nil
}
func (s *Server) localApplyReviewDecision(ctx context.Context, r *pb.ApplyReviewDecisionRequest) (*pb.ApplyReviewDecisionResponse, error) {
	if err := decisionCaller(ctx); err != nil {
		return nil, err
	}
	if err := validateApplyReviewDecision(r); err != nil {
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
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7185429005))`, r.DecisionId); err != nil {
		return nil, storage(err)
	}
	var oldHash, receipt []byte
	err = tx.QueryRow(ctx, `SELECT request_hash,response FROM content.review_decision_receipts WHERE decision_id=$1`, r.DecisionId).Scan(&oldHash, &receipt)
	if err == nil {
		if !bytes.Equal(oldHash, hash[:]) {
			return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
		}
		out := &pb.ApplyReviewDecisionResponse{}
		if protojson.Unmarshal(receipt, out) != nil {
			return nil, status.Error(codes.Internal, "invalid_review_receipt")
		}
		return out, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	var revision int64
	var state string
	var currentReview *string
	err = tx.QueryRow(ctx, `SELECT revision,state,review_id FROM content.task_drafts WHERE id=$1 FOR UPDATE`, r.TaskId).Scan(&revision, &state, &currentReview)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	if revision != r.SubmissionRevision || state != "pending_review" || currentReview == nil || *currentReview != r.ReviewId {
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	var raw []byte
	var reviewState string
	err = tx.QueryRow(ctx, `SELECT snapshot,state FROM content.review_requests WHERE id=$1 AND task_id=$2 AND submission_revision=$3 FOR UPDATE`, r.ReviewId, r.TaskId, r.SubmissionRevision).Scan(&raw, &reviewState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "review_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	if reviewState != "pending_review" {
		return nil, status.Error(codes.Aborted, "review_already_decided")
	}
	snap := &pb.ReviewSnapshot{}
	if protojson.Unmarshal(raw, snap) != nil {
		return nil, status.Error(codes.Internal, "invalid_review_snapshot")
	}
	out := &pb.ApplyReviewDecisionResponse{DecisionId: r.DecisionId, ReviewId: r.ReviewId, Revision: r.SubmissionRevision, State: "rejected"}
	var reason *string
	if r.Outcome == pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
		if len(snap.EntryIds) != len(snap.Submission.Content.Entries) {
			return nil, status.Error(codes.Internal, "invalid_review_snapshot")
		}
		snapshot, e := validate(snap.Submission.Content)
		if e != nil {
			return nil, e
		}
		if e = bundleRequired(snap.Submission.Content); e != nil {
			return nil, e
		}
		var published time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&published); err != nil {
			return nil, storage(err)
		}
		_, err = tx.Exec(ctx, `INSERT INTO content.reviewed_tasks(task_id,source_revision,revision,state,snapshot,published_at) VALUES($1,$2,1,'published',$3,$4)`, r.TaskId, r.SubmissionRevision, snapshot, published)
		if err != nil {
			return nil, storage(err)
		}
		for i, id := range snap.EntryIds {
			_, err = tx.Exec(ctx, `INSERT INTO content.reviewed_entries(id,task_id,position,state,approved_at) VALUES($1,$2,$3,'approved',$4)`, id, r.TaskId, i, published)
			if err != nil {
				return nil, storage(err)
			}
			out.Entries = append(out.Entries, &pb.ReviewedEntry{Id: id, Position: int32(i), State: pb.ReviewedEntryState_REVIEWED_ENTRY_STATE_APPROVED})
		}
		out.State = "published"
	} else {
		v := r.GetReason()
		reason = &v
	}
	_, err = tx.Exec(ctx, `UPDATE content.review_requests SET state=$2,reason=$3 WHERE id=$1`, r.ReviewId, out.State, reason)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE content.task_drafts SET state=$2,rejection_reason=$3,updated_at=clock_timestamp() WHERE id=$1`, r.TaskId, out.State, reason)
	}
	if err != nil {
		return nil, storage(err)
	}
	receipt, err = protojson.Marshal(out)
	if err != nil {
		return nil, status.Error(codes.Internal, "internal_error")
	}
	_, err = tx.Exec(ctx, `INSERT INTO content.review_decision_receipts(decision_id,review_id,request_hash,response) VALUES($1,$2,$3,$4)`, r.DecisionId, r.ReviewId, hash[:], receipt)
	if err != nil {
		return nil, storage(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return out, nil
}
func (s *Server) CheckReviewAssetAccess(ctx context.Context, r *pb.CheckReviewAssetAccessRequest) (*pb.CheckReviewAssetAccessResponse, error) {
	if err := requireContentCaller(ctx, "asset"); err != nil {
		return nil, err
	}
	if s.Identity == nil {
		return nil, status.Error(codes.Unavailable, "identity_unavailable")
	}
	verified, err := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: r.GetActorAssertion(), FullMethod: pb.ContentService_CheckReviewAssetAccess_FullMethodName})
	if err != nil {
		return nil, err
	}
	p := verified.GetPrincipal()
	if p.GetRole() != identitypb.Role_ROLE_ADMIN || p.GetClientKind() != identitypb.ClientKind_CLIENT_KIND_WEB_SESSION {
		return nil, status.Error(codes.PermissionDenied, "administrator_required")
	}
	var allowed bool
	err = s.DB.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM content.review_requests r JOIN content.task_drafts d ON d.id=r.task_id AND d.review_id=r.id WHERE r.id=$1 AND r.state='pending_review' AND d.state='pending_review' AND (r.snapshot->'submission'->'content'->'taskAttachments' @> jsonb_build_array(jsonb_build_object('assetId',$2::text)) OR EXISTS(SELECT 1 FROM jsonb_array_elements(r.snapshot->'submission'->'content'->'entries') e WHERE e->'artifacts' @> jsonb_build_array(jsonb_build_object('kind','file','assetId',$2::text)))))
 OR EXISTS(SELECT 1 FROM content.entry_review_requests r JOIN content.entry_submissions s ON s.id=r.entry_id AND s.review_id=r.id AND s.revision=r.submission_revision JOIN content.reviewed_tasks t ON t.task_id=s.task_id WHERE r.id=$1 AND r.state='pending_review' AND s.state='pending_review' AND t.state='published' AND `+catalogPublic+` AND r.snapshot->'submission'->'content'->'artifacts' @> jsonb_build_array(jsonb_build_object('kind','file','assetId',$2::text)))`, r.ReviewId, r.AssetId).Scan(&allowed)
	if err != nil {
		return nil, storage(err)
	}
	return &pb.CheckReviewAssetAccessResponse{Allowed: allowed}, nil
}

// Deliver one committed outbox item. Lease survives process crashes; RPC is
// outside transactions. A stale holder cannot acknowledge a newer claim.
func (s *Server) DeliverReview(ctx context.Context, client moderationpb.ModerationServiceClient) (bool, error) {
	result, err := s.deliverReview(ctx, client)
	return result.HadWork, err
}

func (s *Server) deliverReview(ctx context.Context, client moderationpb.ModerationServiceClient) (platform.OutboxResult, error) {
	result := platform.OutboxResult{Outcome: "idle", Stage: "claim"}
	if client == nil {
		return result, nil
	}
	token, err := reviewID("lease")
	if err != nil {
		result.Cause = err
		return result, err
	}
	var id string
	err = s.DB.QueryRow(ctx, `UPDATE content.review_outbox o SET lease_token=$1,available_at=clock_timestamp()+interval '15 seconds',attempts=attempts+1 FROM (SELECT id FROM content.review_outbox WHERE delivered_at IS NULL AND available_at<=clock_timestamp() ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) q WHERE o.id=q.id RETURNING o.id`, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		result.Cause = err
		return result, storage(err)
	}
	result.HadWork, result.ItemID, result.Stage = true, id, "load_snapshot"
	var raw []byte
	err = s.DB.QueryRow(ctx, `SELECT snapshot FROM content.review_requests WHERE id=$1`, id).Scan(&raw)
	snap := &pb.ReviewSnapshot{}
	if err == nil {
		result.Stage = "decode_snapshot"
		err = protojson.Unmarshal(raw, snap)
	}
	if err == nil {
		result.Stage = "call_moderation"
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, err = client.CreateReviewCase(call, &moderationpb.CreateReviewCaseRequest{Snapshot: snap})
		cancel()
	}
	if err != nil {
		result.Cause = err
		return result, err
	}
	result.Stage = "confirm_delivery"
	command, err := s.DB.Exec(ctx, `UPDATE content.review_outbox SET delivered_at=clock_timestamp(),lease_token=NULL WHERE id=$1 AND lease_token=$2`, id, token)
	if err != nil {
		result.Cause = err
		return result, storage(err)
	}
	if command.RowsAffected() == 0 {
		result.Outcome = "lease_lost"
		return result, nil
	}
	result.Outcome = "delivered"
	return result, nil
}
func (s *Server) RunReviewDelivery(ctx context.Context, client moderationpb.ModerationServiceClient, logger *slog.Logger) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		platform.RunOutbox(ctx, logger, "content_entry_to_moderation", func(call context.Context) (platform.OutboxResult, error) {
			return s.deliverEntryReview(call, client)
		})
	}()
	defer func() { <-done }()
	platform.RunOutbox(ctx, logger, "content_to_moderation", func(call context.Context) (platform.OutboxResult, error) {
		return s.deliverReview(call, client)
	})
}

func validateApplyReviewDecision(r *pb.ApplyReviewDecisionRequest) error {
	if !keyPattern.MatchString(r.GetDecisionId()) || !taskPattern.MatchString(r.GetTaskId()) || r.GetSubmissionRevision() < 1 {
		return invalid("invalid_review_decision")
	}
	if r.Outcome != pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE && r.Outcome != pb.ReviewOutcome_REVIEW_OUTCOME_REJECT {
		return invalid("invalid_review_decision")
	}
	if r.Outcome == pb.ReviewOutcome_REVIEW_OUTCOME_REJECT && (strings.TrimSpace(r.GetReason()) == "" || len(r.GetReason()) > 4000 || !utf8.ValidString(r.GetReason()) || strings.ContainsRune(r.GetReason(), 0)) {
		return invalid("rejection_reason_required")
	}
	if r.Outcome == pb.ReviewOutcome_REVIEW_OUTCOME_APPROVE && (len(r.GetReason()) > 4000 || !utf8.ValidString(r.GetReason()) || strings.ContainsRune(r.GetReason(), 0)) {
		return invalid("invalid_review_decision")
	}
	return nil
}
