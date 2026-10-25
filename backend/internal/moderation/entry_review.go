package moderation

import (
	"context"
	"encoding/hex"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func validateEntrySnapshot(snap *contentpb.EntryReviewSnapshot) error {
	sub := snap.GetSubmission()
	if !reviewPattern.MatchString(snap.GetReviewId()) || !entryPattern.MatchString(sub.GetId()) || !taskPattern.MatchString(sub.GetTaskId()) || sub.GetAuthorId() == "" || sub.GetRevision() < 1 || (sub.GetOrigin() != "additional" && sub.GetOrigin() != "cloud") || sub.GetState() != "pending_review" || sub.GetReviewId() != snap.GetReviewId() || sub.GetContent() == nil || (sub.Content.Side != "human" && sub.Content.Side != "agent") || sub.RejectionReason != nil || sub.ApprovedAt != nil || proto.Size(snap) > 512<<10 {
		return invalid("invalid_review_snapshot")
	}
	if snap.CreatedAt == nil || snap.CreatedAt.CheckValid() != nil {
		return invalid("invalid_review_snapshot")
	}
	if sub.CreatedAt == nil || sub.CreatedAt.CheckValid() != nil || sub.UpdatedAt == nil || sub.UpdatedAt.CheckValid() != nil {
		return invalid("invalid_review_snapshot")
	}
	return nil
}

func (s *Server) CreateEntryReviewCase(ctx context.Context, r *pb.CreateEntryReviewCaseRequest) (*pb.CreateEntryReviewCaseResponse, error) {
	if err := peer(ctx, "content"); err != nil {
		return nil, err
	}
	snap := r.GetSnapshot()
	if err := validateEntrySnapshot(snap); err != nil {
		return nil, err
	}
	sub := snap.Submission
	review, err := s.createCase(ctx, "entry", snap.ReviewId, sub.TaskId, sub.Id, sub.Revision, snap.CreatedAt, snap)
	if err != nil {
		return nil, err
	}
	return &pb.CreateEntryReviewCaseResponse{Review: review}, nil
}

// The durable hash belongs to the submitted snapshot, never the current entry.
// Task and entry receipts retain independent types and state vocabularies.
func (s *Server) applyDecision(ctx context.Context, r *storedReview) (proto.Message, error) {
	if snap := r.EntrySnapshot; snap != nil {
		if len(r.snapshotHash) != 32 {
			return nil, status.Error(codes.Unavailable, "invalid_review_snapshot_hash")
		}
		sub := snap.Submission
		request := &contentpb.ApplyEntryReviewDecisionRequest{
			DecisionId: r.GetDecisionId(), ReviewId: r.Id, TaskId: sub.TaskId,
			EntryId: sub.Id, SubmissionRevision: sub.Revision, Outcome: r.Decision,
			Reason: r.Reason, SnapshotSha256: hex.EncodeToString(r.snapshotHash),
		}
		receipt, err := s.Content.ApplyEntryReviewDecision(ctx, request)
		if err != nil {
			return nil, err
		}
		state := "rejected"
		if r.Decision == contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
			state = "approved"
		}
		if receipt.GetDecisionId() != request.DecisionId || receipt.GetReviewId() != r.Id || receipt.GetEntryId() != sub.Id || receipt.GetRevision() != sub.Revision || receipt.GetState() != state {
			return nil, status.Error(codes.Unavailable, "invalid_review_receipt")
		}
		return receipt, nil
	}
	sub := r.Snapshot.GetSubmission()
	request := &contentpb.ApplyReviewDecisionRequest{DecisionId: r.GetDecisionId(), ReviewId: r.Id, TaskId: sub.GetId(), SubmissionRevision: sub.GetRevision(), Outcome: r.Decision, Reason: r.Reason}
	receipt, err := s.Content.ApplyReviewDecision(ctx, request)
	if err != nil {
		return nil, err
	}
	state := "rejected"
	if r.Decision == contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
		state = "published"
	}
	if receipt.GetDecisionId() != request.DecisionId || receipt.GetReviewId() != r.Id || receipt.GetRevision() != request.SubmissionRevision || receipt.GetState() != state {
		return nil, status.Error(codes.Unavailable, "invalid_review_receipt")
	}
	return receipt, nil
}
