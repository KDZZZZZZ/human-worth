package voting

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"time"

	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	pb.UnimplementedVotingServiceServer
	DB       *pgxpool.Pool
	Identity identitypb.IdentityServiceClient
	Content  contentpb.ContentServiceClient
}

// ponytail: two bounded complete-catalog operations per process. Return a
// deadline-bounded wait before loading/encoding complete candidate sets.
var catalogWork = make(chan struct{}, 2)

var taskID = regexp.MustCompile(`^tsk_[a-f0-9]{32}$`)
var objectID = regexp.MustCompile(`^[a-z]+_[a-f0-9]{32}$`)

func invalid(reason string) error  { return status.Error(codes.InvalidArgument, reason) }
func conflict(reason string) error { return status.Error(codes.Aborted, reason) }
func storage(err error) error {
	if err == nil {
		return nil
	}
	return platform.DependencyError(err, "voting_unavailable")
}
func id(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", status.Error(codes.Internal, "internal_error")
	}
	return fmt.Sprintf("%s_%x", prefix, b), nil
}
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
	switch info.FullMethod {
	case pb.VotingService_GetPublicConclusions_FullMethodName:
		expected = "discovery"
	case pb.VotingService_PrepareCatalogChange_FullMethodName, pb.VotingService_ActivateCatalogSnapshot_FullMethodName, pb.VotingService_GetCatalogTransition_FullMethodName:
		expected = "content"
	case pb.VotingService_GetHumanPreferenceSnapshot_FullMethodName, pb.VotingService_CheckHumanPreferenceSnapshot_FullMethodName:
		expected = "challenge"
	case "/grpc.health.v1.Health/Check":
		name, err := platform.ServiceName(ctx)
		if err == nil && name == "content" {
			return next(ctx, r)
		}
	}
	if err := peer(ctx, expected); err != nil {
		return nil, err
	}
	select {
	case catalogWork <- struct{}{}:
		defer func() { <-catalogWork }()
	case <-ctx.Done():
		return nil, platform.ContextError(ctx.Err())
	}
	return next(ctx, r)
}

// No streaming RPC is supported by Voting. The standard Health service also
// registers Watch; do not let it bypass service authorization or hold a stream
// indefinitely merely because the caller owns a certificate signed by our CA.
func AuthorizationStream(_ any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, _ grpc.StreamHandler) error {
	if err := peer(stream.Context(), "gateway"); err != nil {
		return err
	}
	return status.Error(codes.Unimplemented, "streaming_not_supported")
}

func (s *Server) actor(ctx context.Context, assertion, method string, mcp bool) (string, error) {
	if err := peer(ctx, "gateway"); err != nil {
		return "", err
	}
	if s.Identity == nil {
		return "", status.Error(codes.Unavailable, "identity_unavailable")
	}
	result, err := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return "", err
	}
	p := result.GetPrincipal()
	if p.GetAccountId() == "" {
		return "", status.Error(codes.Unauthenticated, "authentication_required")
	}
	if p.ClientKind != identitypb.ClientKind_CLIENT_KIND_WEB_SESSION && !(mcp && p.ClientKind == identitypb.ClientKind_CLIENT_KIND_MCP_READ) {
		return "", status.Error(codes.PermissionDenied, "credential_purpose_forbidden")
	}
	return p.AccountId, nil
}
func (s *Server) authority(ctx context.Context, task string) (*contentpb.VotingCatalogSnapshot, error) {
	if !taskID.MatchString(task) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if s.Content == nil {
		return nil, status.Error(codes.Unavailable, "content_unavailable")
	}
	out, err := s.Content.GetVotingCatalogSnapshot(ctx, &contentpb.GetVotingCatalogSnapshotRequest{TaskId: task})
	if err != nil {
		return nil, err
	}
	c := out.GetCatalog()
	if contract.ValidateCatalog(c) != nil {
		return nil, status.Error(codes.Unavailable, "invalid_catalog")
	}
	if c.State != "open" {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	return c, nil
}
func gate(ctx context.Context, tx pgx.Tx, expected *contentpb.VotingCatalogSnapshot, exclusive ...bool) (*contentpb.VotingCatalogSnapshot, error) {
	var raw []byte
	var state, hash string
	var generation int64
	lock := " FOR SHARE"
	if len(exclusive) > 0 && exclusive[0] {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, `SELECT state,generation,sha256,snapshot FROM voting.catalog_gates WHERE task_id=$1`+lock, expected.TaskId).Scan(&state, &generation, &hash, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.Unavailable, "catalog_not_initialized")
	}
	if err != nil {
		return nil, storage(err)
	}
	if state != "open" || generation != expected.Generation || hash != expected.Sha256 {
		return nil, conflict("catalog_changed")
	}
	c := &contentpb.VotingCatalogSnapshot{}
	if proto.Unmarshal(raw, c) != nil || contract.ValidateCatalog(c) != nil {
		return nil, status.Error(codes.Unavailable, "invalid_catalog")
	}
	return c, nil
}
func guard(ctx context.Context, tx pgx.Tx, account, task string) (*time.Time, error) {
	_, err := tx.Exec(ctx, `INSERT INTO voting.account_task_guards(account_id,task_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, account, task)
	if err != nil {
		return nil, storage(err)
	}
	var ignored string
	if err = tx.QueryRow(ctx, `SELECT account_id FROM voting.account_task_guards WHERE account_id=$1 AND task_id=$2 FOR UPDATE`, account, task).Scan(&ignored); err != nil {
		return nil, storage(err)
	}
	var closed time.Time
	err = tx.QueryRow(ctx, `SELECT closed_at FROM voting.statistics_access WHERE account_id=$1 AND task_id=$2`, account, task).Scan(&closed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storage(err)
	}
	return &closed, nil
}
func eligibility(task string, closed *time.Time) *pb.VotingEligibility {
	out := &pb.VotingEligibility{TaskId: task, CanVote: closed == nil, PermanentlyClosed: closed != nil}
	if closed != nil {
		out.ClosedAt = timestamppb.New(*closed)
		reason := "statistics_viewed"
		out.Reason = &reason
	}
	return out
}
func currentVote(ctx context.Context, tx pgx.Tx, account, task string) (*pb.Vote, []byte, error) {
	out := &pb.Vote{TaskId: task}
	var created, updated time.Time
	var hash []byte
	err := tx.QueryRow(ctx, `SELECT id,entry_id,revision,created_at,updated_at,request_hash FROM voting.current_votes WHERE account_id=$1 AND task_id=$2`, account, task).Scan(&out.Id, &out.EntryId, &out.Revision, &created, &updated, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, storage(err)
	}
	out.CreatedAt = timestamppb.New(created)
	out.UpdatedAt = timestamppb.New(updated)
	return out, hash, nil
}
func (s *Server) GetVotingEligibility(ctx context.Context, r *pb.GetVotingEligibilityRequest) (*pb.GetVotingEligibilityResponse, error) {
	account, err := s.actor(ctx, r.GetActorAssertion(), pb.VotingService_GetVotingEligibility_FullMethodName, true)
	if err != nil {
		return nil, err
	}
	c, err := s.authority(ctx, r.GetTaskId())
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	if _, err = gate(ctx, tx, c); err != nil {
		return nil, err
	}
	// Read-only: no guard/closure row created by this GET.
	var closed *time.Time
	err = tx.QueryRow(ctx, `SELECT (SELECT closed_at FROM voting.statistics_access WHERE account_id=$1 AND task_id=$2)`, account, c.TaskId).Scan(&closed)
	if err != nil {
		return nil, storage(err)
	}
	return &pb.GetVotingEligibilityResponse{Eligibility: eligibility(c.TaskId, closed)}, nil
}
func (s *Server) GetBallot(ctx context.Context, r *pb.GetBallotRequest) (*pb.GetBallotResponse, error) {
	account, err := s.actor(ctx, r.GetActorAssertion(), pb.VotingService_GetBallot_FullMethodName, false)
	if err != nil {
		return nil, err
	}
	authority, err := s.authority(ctx, r.GetTaskId())
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	c, err := gate(ctx, tx, authority)
	if err != nil {
		return nil, err
	}
	closed, err := guard(ctx, tx, account, c.TaskId)
	if err != nil {
		return nil, err
	}
	if closed != nil {
		return nil, status.Error(codes.PermissionDenied, "voting_permanently_closed")
	}
	vote, _, err := currentVote(ctx, tx, account, c.TaskId)
	if err != nil {
		return nil, err
	}
	ballot, err := id("bal")
	if err != nil {
		return nil, err
	}
	var expires time.Time
	err = tx.QueryRow(ctx, `INSERT INTO voting.ballots(id,account_id,task_id,generation,sha256) VALUES($1,$2,$3,$4,$5) RETURNING expires_at`, ballot, account, c.TaskId, c.Generation, c.Sha256).Scan(&expires)
	if err != nil {
		return nil, storage(err)
	}
	out := &pb.GetBallotResponse{Ballot: &pb.Ballot{Id: ballot, TaskId: c.TaskId, CatalogRevision: c.CatalogRevision, Generation: c.Generation, Entries: c.Entries, CurrentVote: vote, ExpiresAt: timestamppb.New(expires)}}
	if proto.Size(out) > contract.VotingCatalogBytes {
		return nil, status.Error(codes.ResourceExhausted, "catalog_too_large")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return out, nil
}
func (s *Server) CastVote(ctx context.Context, r *pb.CastVoteRequest) (*pb.CastVoteResponse, error) {
	account, err := s.actor(ctx, r.GetActorAssertion(), pb.VotingService_CastVote_FullMethodName, false)
	if err != nil {
		return nil, err
	}
	if !objectID.MatchString(r.GetBallotId()) || !objectID.MatchString(r.GetEntryId()) || r.GetExpectedVoteRevision() < 0 {
		return nil, invalid("invalid_vote")
	}
	c, err := s.authority(ctx, r.GetTaskId())
	if err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	c, err = gate(ctx, tx, c, true)
	if err != nil {
		return nil, err
	}
	closed, err := guard(ctx, tx, account, c.TaskId)
	if err != nil {
		return nil, err
	}
	if closed != nil {
		return nil, status.Error(codes.PermissionDenied, "voting_permanently_closed")
	}
	// Do not include short-lived assertion bytes in the persistent idempotency key.
	copy := proto.Clone(r).(*pb.CastVoteRequest)
	copy.ActorAssertion = ""
	raw, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(copy)
	hash := sha256.Sum256(raw)
	vote, oldHash, err := currentVote(ctx, tx, account, c.TaskId)
	if err != nil {
		return nil, err
	}
	var generation int64
	var digest string
	var valid bool
	err = tx.QueryRow(ctx, `SELECT generation,sha256,expires_at>clock_timestamp() FROM voting.ballots WHERE id=$1 AND account_id=$2 AND task_id=$3`, r.BallotId, account, c.TaskId).Scan(&generation, &digest, &valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, conflict("invalid_ballot")
	}
	if err != nil {
		return nil, storage(err)
	}
	if generation != c.Generation || digest != c.Sha256 {
		return nil, conflict("ballot_stale")
	}
	if vote != nil && bytes.Equal(oldHash, hash[:]) {
		return &pb.CastVoteResponse{Vote: vote}, nil
	}
	if !valid {
		return nil, conflict("ballot_expired")
	}
	revision := int64(0)
	if vote != nil {
		revision = vote.Revision
	}
	if revision != r.ExpectedVoteRevision {
		return nil, conflict("vote_revision_conflict")
	}
	found := false
	for _, e := range c.Entries {
		found = found || e.Id == r.EntryId
	}
	if !found {
		return nil, conflict("entry_not_in_ballot")
	}
	if vote == nil {
		v, err := id("vot")
		if err != nil {
			return nil, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO voting.current_votes(account_id,task_id,id,entry_id,revision,request_hash,generation) VALUES($1,$2,$3,$4,1,$5,$6)`, account, c.TaskId, v, r.EntryId, hash[:], c.Generation)
		if err != nil {
			return nil, storage(err)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE voting.current_votes SET entry_id=$3,revision=revision+1,request_hash=$4,generation=$5,updated_at=clock_timestamp() WHERE account_id=$1 AND task_id=$2`, account, c.TaskId, r.EntryId, hash[:], c.Generation)
		if err != nil {
			return nil, storage(err)
		}
	}
	vote, _, err = currentVote(ctx, tx, account, c.TaskId)
	if err != nil {
		return nil, err
	}
	if err = updateConclusion(ctx, tx, c); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.CastVoteResponse{Vote: vote}, nil
}
