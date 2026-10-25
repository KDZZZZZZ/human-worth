package moderation

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	pb.UnimplementedModerationServiceServer
	DB       *pgxpool.Pool
	Identity identitypb.IdentityServiceClient
	Content  contentpb.ContentServiceClient
}

var reviewPattern = regexp.MustCompile(`^rev_[a-f0-9]{32}$`)
var taskPattern = regexp.MustCompile(`^tsk_[a-f0-9]{32}$`)
var entryPattern = regexp.MustCompile(`^ent_[a-f0-9]{32}$`)

func peer(ctx context.Context, expected string) error {
	name, err := platform.ServiceName(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if name != expected {
		return status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return nil
}
func Authorization(ctx context.Context, r any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	expected := "gateway"
	if info.FullMethod == pb.ModerationService_CreateReviewCase_FullMethodName || info.FullMethod == pb.ModerationService_CreateEntryReviewCase_FullMethodName {
		expected = "content"
	}
	if err := peer(ctx, expected); err != nil {
		return nil, err
	}
	return next(ctx, r)
}
func AuthorizationStream(service any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	return status.Error(codes.PermissionDenied, "service_forbidden")
}
func unavailable(err error) error {
	return platform.DependencyError(err, "moderation_unavailable")
}
func invalid(s string) error { return status.Error(codes.InvalidArgument, s) }
func (s *Server) admin(ctx context.Context, assertion, method string) (string, error) {
	if err := peer(ctx, "gateway"); err != nil {
		return "", err
	}
	if s.Identity == nil {
		return "", status.Error(codes.Unavailable, "identity_unavailable")
	}
	v, err := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return "", err
	}
	p := v.GetPrincipal()
	if p.GetClientKind() != identitypb.ClientKind_CLIENT_KIND_WEB_SESSION || p.GetRole() != identitypb.Role_ROLE_ADMIN || p.GetAccountId() == "" {
		return "", status.Error(codes.PermissionDenied, "administrator_required")
	}
	return p.AccountId, nil
}

const reviewSelect = `SELECT id,snapshot,status,reason,reviewer_id,created_at,decided_at,decision,delivery_state,receipt,decision_id,object_kind,snapshot_hash FROM moderation.reviews`

type scanner interface{ Scan(...any) error }

type storedReview struct {
	*pb.Review
	snapshotHash []byte
}

func readReview(row scanner) (*storedReview, error) {
	r := &storedReview{Review: &pb.Review{}}
	var raw, receipt []byte
	var created time.Time
	var decided *time.Time
	var decision *string
	var delivery, kind string
	err := row.Scan(&r.Id, &raw, &r.Status, &r.Reason, &r.ReviewerId, &created, &decided, &decision, &delivery, &receipt, &r.DecisionId, &kind, &r.snapshotHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "review_not_found")
	}
	if err != nil {
		return nil, unavailable(err)
	}
	var snapshot proto.Message
	switch kind {
	case "task":
		r.Snapshot = &contentpb.ReviewSnapshot{}
		snapshot = r.Snapshot
	case "entry":
		r.EntrySnapshot = &contentpb.EntryReviewSnapshot{}
		snapshot = r.EntrySnapshot
	default:
		return nil, status.Error(codes.Internal, "invalid_review_snapshot")
	}
	if protojson.Unmarshal(raw, snapshot) != nil {
		return nil, status.Error(codes.Internal, "invalid_review_snapshot")
	}
	r.CreatedAt = timestamppb.New(created)
	if decided != nil {
		r.DecidedAt = timestamppb.New(*decided)
	}
	if decision != nil {
		r.Decision = contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE
		if *decision == "reject" {
			r.Decision = contentpb.ReviewOutcome_REVIEW_OUTCOME_REJECT
		}
	}
	r.DeliveryState = map[string]pb.DeliveryState{"undecided": pb.DeliveryState_DELIVERY_STATE_UNDECIDED, "pending": pb.DeliveryState_DELIVERY_STATE_PENDING, "applied": pb.DeliveryState_DELIVERY_STATE_APPLIED, "blocked": pb.DeliveryState_DELIVERY_STATE_BLOCKED}[delivery]
	if len(receipt) > 0 {
		var response proto.Message
		if kind == "task" {
			r.Receipt = &contentpb.ApplyReviewDecisionResponse{}
			response = r.Receipt
		} else {
			r.EntryReceipt = &contentpb.ApplyEntryReviewDecisionResponse{}
			response = r.EntryReceipt
		}
		if protojson.Unmarshal(receipt, response) != nil {
			return nil, status.Error(codes.Internal, "invalid_review_receipt")
		}
	}
	return r, nil
}
func validateSnapshot(snap *contentpb.ReviewSnapshot) error {
	sub := snap.GetSubmission()
	if !reviewPattern.MatchString(snap.GetReviewId()) || !taskPattern.MatchString(sub.GetId()) || sub.GetAuthorId() == "" || sub.GetRevision() < 1 || sub.GetState() != "pending_review" || sub.GetReviewId() != snap.GetReviewId() || sub.GetContent() == nil || len(snap.GetEntryIds()) != len(sub.GetContent().GetEntries()) || len(snap.GetEntryIds()) > 32 || proto.Size(snap) > 512<<10 || snap.GetCreatedAt() == nil || snap.CreatedAt.CheckValid() != nil {
		return invalid("invalid_review_snapshot")
	}
	seen := map[string]bool{}
	human, agent := false, false
	for i, id := range snap.EntryIds {
		if !entryPattern.MatchString(id) || seen[id] {
			return invalid("invalid_review_snapshot")
		}
		seen[id] = true
		e := sub.Content.Entries[i]
		human = human || e.GetSide() == "human"
		agent = agent || e.GetSide() == "agent"
	}
	if !human || !agent {
		return invalid("invalid_review_snapshot")
	}
	return nil
}
func (s *Server) CreateReviewCase(ctx context.Context, r *pb.CreateReviewCaseRequest) (*pb.CreateReviewCaseResponse, error) {
	if err := peer(ctx, "content"); err != nil {
		return nil, err
	}
	snap := r.GetSnapshot()
	if err := validateSnapshot(snap); err != nil {
		return nil, err
	}
	review, err := s.createCase(ctx, "task", snap.ReviewId, snap.Submission.Id, snap.Submission.Id, snap.Submission.Revision, snap.CreatedAt, snap)
	if err != nil {
		return nil, err
	}
	return &pb.CreateReviewCaseResponse{Review: review}, nil
}

func (s *Server) createCase(ctx context.Context, kind, id, task, object string, revision int64, created *timestamppb.Timestamp, snap proto.Message) (*pb.Review, error) {
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(snap)
	if err != nil {
		return nil, invalid("invalid_review_snapshot")
	}
	hash := sha256.Sum256(wire)
	raw, err := protojson.Marshal(snap)
	if err != nil {
		return nil, invalid("invalid_review_snapshot")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO moderation.reviews(id,task_id,submission_revision,snapshot,snapshot_hash,created_at,object_kind) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, id, task, revision, raw, hash[:], created.AsTime(), kind)
	if err != nil {
		return nil, unavailable(err)
	}
	var old []byte
	err = tx.QueryRow(ctx, `SELECT snapshot_hash FROM moderation.reviews WHERE id=$1 AND object_kind=$2 AND object_id=$3 AND task_id=$4 AND submission_revision=$5`, id, kind, object, task, revision).Scan(&old)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !bytes.Equal(old, hash[:]) {
		return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
	}
	if err != nil {
		return nil, unavailable(err)
	}
	review, err := readReview(tx.QueryRow(ctx, reviewSelect+` WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, unavailable(err)
	}
	return review.Review, nil
}
func (s *Server) GetReview(ctx context.Context, r *pb.GetReviewRequest) (*pb.GetReviewResponse, error) {
	if _, err := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_GetReview_FullMethodName); err != nil {
		return nil, err
	}
	review, err := readReview(s.DB.QueryRow(ctx, reviewSelect+` WHERE id=$1`, r.GetReviewId()))
	if err != nil {
		return nil, err
	}
	return &pb.GetReviewResponse{Review: review.Review}, nil
}
func decisionInput(outcome contentpb.ReviewOutcome, reason *string) (string, *string, error) {
	if outcome == contentpb.ReviewOutcome_REVIEW_OUTCOME_APPROVE {
		if reason != nil && (!utf8.ValidString(*reason) || strings.ContainsRune(*reason, 0) || len(*reason) > 4000) {
			return "", nil, invalid("invalid_review_decision")
		}
		return "approve", reason, nil
	}
	if outcome != contentpb.ReviewOutcome_REVIEW_OUTCOME_REJECT {
		return "", nil, invalid("invalid_review_decision")
	}
	if reason == nil || strings.TrimSpace(*reason) == "" || !utf8.ValidString(*reason) || strings.ContainsRune(*reason, 0) || len(*reason) > 4000 {
		return "", nil, invalid("rejection_reason_required")
	}
	return "reject", reason, nil
}
func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", status.Error(codes.Internal, "internal_error")
	}
	return fmt.Sprintf("%s_%x", prefix, b), nil
}
func (s *Server) DecideReview(ctx context.Context, r *pb.DecideReviewRequest) (*pb.DecideReviewResponse, error) {
	admin, err := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_DecideReview_FullMethodName)
	if err != nil {
		return nil, err
	}
	decision, reason, err := decisionInput(r.Decision, r.Reason)
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, unavailable(err)
	}
	defer tx.Rollback(ctx)
	old, err := readReview(tx.QueryRow(ctx, reviewSelect+` WHERE id=$1 FOR UPDATE`, r.GetReviewId()))
	if err != nil {
		return nil, err
	}
	if old.Decision != contentpb.ReviewOutcome_REVIEW_OUTCOME_UNSPECIFIED {
		if old.Decision != r.Decision || old.GetReason() != r.GetReason() {
			return nil, status.Error(codes.Aborted, "review_already_decided")
		}
		return &pb.DecideReviewResponse{Review: old.Review}, nil
	}
	id, err := randomID("dec")
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE moderation.reviews SET decision=$2,decision_id=$3,reason=$4,reviewer_id=$5,decided_at=clock_timestamp(),delivery_state='pending' WHERE id=$1`, old.Id, decision, id, reason, admin)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO moderation.decision_outbox(id) VALUES($1)`, old.Id)
	}
	if err != nil {
		return nil, unavailable(err)
	}
	result, err := readReview(tx.QueryRow(ctx, reviewSelect+` WHERE id=$1`, old.Id))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, unavailable(err)
	}
	return &pb.DecideReviewResponse{Review: result.Review}, nil
}

type cursor struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	Created string `json:"created"`
	ID      string `json:"id"`
}

func scope(admin, state string) string {
	hash := sha256.Sum256([]byte(admin + "\x00" + state + "\x00created_desc_id_desc"))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func encodeCursor(admin, state string, r *pb.Review) string {
	raw, _ := json.Marshal(cursor{1, scope(admin, state), r.CreatedAt.AsTime().UTC().Format(time.RFC3339Nano), r.Id})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeCursor(raw, admin, state string) (time.Time, string, error) {
	if len(raw) > 2048 {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	var c cursor
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Scope != scope(admin, state) || !reviewPattern.MatchString(c.ID) {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, c.Created)
	if err != nil || created.UTC().Format(time.RFC3339Nano) != c.Created {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	return created, c.ID, nil
}
func (s *Server) ListReviews(ctx context.Context, r *pb.ListReviewsRequest) (*pb.ListReviewsResponse, error) {
	admin, err := s.admin(ctx, r.GetActorAssertion(), pb.ModerationService_ListReviews_FullMethodName)
	if err != nil {
		return nil, err
	}
	state := r.Status
	if state == "" {
		state = "pending"
	}
	if state != "pending" && state != "approved" && state != "rejected" {
		return nil, invalid("invalid_request")
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("invalid_request")
	}
	query := reviewSelect + ` WHERE status=$1`
	args := []any{state}
	if r.Cursor != "" {
		created, id, err := decodeCursor(r.Cursor, admin, state)
		if err != nil {
			return nil, err
		}
		query += ` AND (created_at,id)<($2,$3)`
		args = append(args, created, id)
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	out := &pb.ListReviewsResponse{Items: make([]*pb.Review, 0, limit)}
	size := 0
	for rows.Next() {
		if len(out.Items) == int(limit) {
			return out, nil
		}
		review, err := readReview(rows)
		if err != nil {
			return nil, err
		}
		next := encodeCursor(admin, state, review.Review)
		entrySize := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(review.Review))
		cursorSize := protowire.SizeTag(2) + protowire.SizeBytes(len(next))
		if size+entrySize+cursorSize > 2<<20 {
			if len(out.Items) == 0 {
				return nil, status.Error(codes.Internal, "stored_review_too_large")
			}
			return out, nil
		}
		size += entrySize
		out.Items = append(out.Items, review.Review)
		out.NextCursor = next
	}
	if err = rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	out.NextCursor = ""
	return out, nil
}
func (s *Server) DeliverDecision(ctx context.Context) (bool, error) {
	result, err := s.deliverDecision(ctx)
	return result.HadWork, err
}

func (s *Server) deliverDecision(ctx context.Context) (platform.OutboxResult, error) {
	result := platform.OutboxResult{Outcome: "idle", Stage: "claim"}
	if s.Content == nil {
		return result, nil
	}
	token, err := randomID("lease")
	if err != nil {
		result.Cause = err
		return result, err
	}
	var id string
	err = s.DB.QueryRow(ctx, `UPDATE moderation.decision_outbox o SET lease_token=$1,available_at=clock_timestamp()+interval '15 seconds',attempts=attempts+1 FROM (SELECT id FROM moderation.decision_outbox WHERE delivered_at IS NULL AND NOT blocked AND available_at<=clock_timestamp() ORDER BY available_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) q WHERE o.id=q.id RETURNING o.id`, token).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		result.Cause = err
		return result, unavailable(err)
	}
	result.HadWork, result.ItemID, result.Stage = true, id, "load_review"
	r, err := readReview(s.DB.QueryRow(ctx, reviewSelect+` WHERE id=$1`, id))
	if err != nil {
		result.Cause = err
		return result, err
	}
	result.Stage = "call_content"
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	receipt, err := s.applyDecision(call, r)
	cancel()
	blocked := false
	if err != nil {
		switch status.Code(err) {
		case codes.Aborted, codes.AlreadyExists, codes.InvalidArgument, codes.FailedPrecondition:
			blocked = true
			result.Reason = status.Convert(err).Message()
		default:
			if status.Convert(err).Message() == "invalid_review_receipt" {
				result.Stage = "validate_receipt"
			}
			result.Cause = err
			return result, err
		}
	}
	result.Stage = "confirm_lease"
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		result.Cause = e
		return result, unavailable(e)
	}
	defer tx.Rollback(ctx)
	var held *string
	e = tx.QueryRow(ctx, `SELECT lease_token FROM moderation.decision_outbox WHERE id=$1 FOR UPDATE`, id).Scan(&held)
	if e != nil {
		result.Cause = e
		return result, unavailable(e)
	}
	if held == nil || *held != token {
		result.Outcome = "lease_lost"
		return result, nil
	}
	if blocked {
		result.Stage = "persist_blocked"
		_, e = tx.Exec(ctx, `UPDATE moderation.reviews SET delivery_state='blocked' WHERE id=$1`, id)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE moderation.decision_outbox SET blocked=true,lease_token=NULL WHERE id=$1`, id)
		}
	} else {
		result.Stage = "persist_applied"
		raw, marshalErr := protojson.Marshal(receipt)
		if marshalErr != nil {
			result.Cause = marshalErr
			return result, status.Error(codes.Internal, "internal_error")
		}
		final := "approved"
		if r.Decision == contentpb.ReviewOutcome_REVIEW_OUTCOME_REJECT {
			final = "rejected"
		}
		_, e = tx.Exec(ctx, `UPDATE moderation.reviews SET status=$2,delivery_state='applied',receipt=$3 WHERE id=$1`, id, final, raw)
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE moderation.decision_outbox SET delivered_at=clock_timestamp(),lease_token=NULL WHERE id=$1`, id)
		}
	}
	if e != nil {
		result.Cause = e
		return result, unavailable(e)
	}
	if e = tx.Commit(ctx); e != nil {
		result.Cause = e
		return result, unavailable(e)
	}
	if blocked {
		result.Outcome = "blocked"
		result.Stage = "downstream_business_conflict"
		result.Cause = err
		return result, nil
	}
	result.Outcome = "delivered"
	return result, nil
}
func (s *Server) RunDelivery(ctx context.Context, logger *slog.Logger) {
	go s.RunActionDelivery(ctx, logger)
	platform.RunOutbox(ctx, logger, "moderation_to_content", func(call context.Context) (platform.OutboxResult, error) {
		return s.deliverDecision(call)
	})
}
