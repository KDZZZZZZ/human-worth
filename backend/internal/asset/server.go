package asset

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	assetapp "github.com/KDZZZZZZ/human-worth/backend/internal/asset/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxAssetBytes  int64 = 32 << 20
	maxChunkBytes        = platform.AssetUploadChunkBytes
	maxAssetCursor       = 2048
	assetPageLimit       = 100
	assetTTL             = 24 * time.Hour
)

var (
	keyPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
	uploadPattern  = regexp.MustCompile(`^upl_[a-f0-9]{32}$`)
	assetPattern   = regexp.MustCompile(`^ast_[a-f0-9]{32}$`)
	attemptPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
)

type Server struct {
	pb.UnimplementedAssetServiceServer
	DB            *pgxpool.Pool
	Identity      identitypb.IdentityServiceClient
	Storage       Storage
	Content       contentpb.ContentServiceClient
	BrowserUpload interface {
		WriteChunk(context.Context, string, string, int64, []byte) (int64, error)
	}
	UploadSessions interface {
		Create(context.Context, assetapp.CreateSessionCommand) (assetapp.Asset, assetapp.Session, error)
		Get(context.Context, string, string) (assetapp.Session, error)
	}
	CompleteUseCase interface {
		Execute(context.Context, string, string, string) (assetapp.Asset, error)
	}
	AbortUseCase interface {
		Execute(context.Context, string, string) (assetapp.Session, error)
	}
	ChallengeUpload interface {
		Execute(context.Context, assetapp.ChallengeUpload, []byte, assetapp.ChallengeFrameSource) (assetapp.Asset, error)
	}
	uploadOnce  sync.Once
	uploadSlots chan struct{}
}

func invalid(reason string) error { return status.Error(codes.InvalidArgument, reason) }
func mapUploadError(err error) error {
	var fault *assetapp.Error
	if !errors.As(err, &fault) {
		return unavailable(err)
	}
	var code codes.Code
	switch fault.Code {
	case "invalid_argument":
		code = codes.InvalidArgument
	case "not_found":
		code = codes.NotFound
	case "failed_precondition":
		code = codes.FailedPrecondition
	case "aborted":
		code = codes.Aborted
	case "already_exists":
		code = codes.AlreadyExists
	case "internal":
		code = codes.Internal
	case "canceled":
		code = codes.Canceled
	case "deadline_exceeded":
		code = codes.DeadlineExceeded
	default:
		code = codes.Unavailable
	}
	return status.Error(code, fault.Reason)
}
func unavailable(err error) error {
	return platform.DependencyError(err, "asset_unavailable")
}
func storageReady(s *Server) error {
	if s.Storage == nil {
		return status.Error(codes.Unavailable, "storage_unavailable")
	}
	return nil
}
func assetPeer(ctx context.Context, method string) error {
	name, err := platform.ServiceName(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if (method == "/grpc.health.v1.Health/Check" && (name == "gateway" || name == "content" || name == "challenge")) ||
		(method == "/grpc.health.v1.Health/Watch" && (name == "gateway" || name == "content")) {
		return nil
	}
	switch name {
	case "gateway":
		switch method {
		case pb.AssetService_CreateUploadSession_FullMethodName,
			pb.AssetService_UploadAsset_FullMethodName,
			pb.AssetService_CompleteUpload_FullMethodName,
			pb.AssetService_AbortUpload_FullMethodName,
			pb.AssetService_GetUploadSession_FullMethodName,
			pb.AssetService_GetAssetMetadata_FullMethodName,
			pb.AssetService_ListMyAssets_FullMethodName,
			pb.AssetService_DownloadAsset_FullMethodName,
			pb.AssetService_DownloadPublicAsset_FullMethodName, pb.AssetService_DownloadReviewAsset_FullMethodName:
			return nil
		}
	case "content":
		if method == pb.AssetService_CheckAssetOwnership_FullMethodName || method == pb.AssetService_GetAsset_FullMethodName {
			return nil
		}
	case "challenge":
		if method == pb.AssetService_GetAsset_FullMethodName || method == pb.AssetService_ReadAsset_FullMethodName || method == pb.AssetService_UploadAsset_FullMethodName {
			return nil
		}
	}
	return status.Error(codes.PermissionDenied, "service_forbidden")
}

func requireAssetCaller(ctx context.Context, expected string) error {
	name, err := platform.ServiceName(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if name != expected {
		return status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return nil
}
func (s *Server) actor(ctx context.Context, assertion, method string) (string, error) {
	if err := assetPeer(ctx, method); err != nil {
		return "", err
	}
	if s.Identity == nil {
		return "", status.Error(codes.Unavailable, "identity_unavailable")
	}
	response, err := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return "", err
	}
	principal := response.GetPrincipal()
	if principal.GetClientKind() != identitypb.ClientKind_CLIENT_KIND_WEB_SESSION || principal.GetAccountId() == "" {
		return "", status.Error(codes.PermissionDenied, "web_session_required")
	}
	return principal.AccountId, nil
}

func Authorization(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if err := assetPeer(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return next(ctx, request)
}
func AuthorizationStream(service any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	if err := assetPeer(stream.Context(), info.FullMethod); err != nil {
		return err
	}
	return next(service, stream)
}

type assetScan interface{ Scan(...any) error }
type assetRow struct {
	ID, Owner, RunID, AttemptID, UploadID, State, Filename, Declared, Detected, StagingKey, FinalKey string
	Expected, Received                                                                               int64
	ExpectedHash, ActualHash                                                                         []byte
	Created, Completed                                                                               *time.Time
}

func scanAsset(row assetScan) (assetRow, error) {
	var result assetRow
	var detected *string
	err := row.Scan(&result.ID, &result.Owner, &result.RunID, &result.AttemptID, &result.UploadID, &result.State, &result.Filename, &result.Declared, &detected, &result.Expected, &result.Received, &result.ExpectedHash, &result.ActualHash, &result.StagingKey, &result.FinalKey, &result.Created, &result.Completed)
	if detected != nil {
		result.Detected = *detected
	}
	return result, err
}
func assetContract(row assetRow) *pb.Asset {
	return &pb.Asset{Id: row.ID, Filename: row.Filename, MediaType: row.Detected, Bytes: row.Expected, Sha256: hex.EncodeToString(row.ActualHash), RunId: row.RunID, AttemptId: row.AttemptID}
}

var challengePurposes = map[string]bool{
	"WORK_KIND_VALIDATE_RANKER":  true,
	"WORK_KIND_TRAIN_RANKER":     true,
	"WORK_KIND_REFINE_RANKER":    true,
	"WORK_KIND_PACK_TASK":        true,
	"WORK_KIND_EXECUTE_PACKAGE":  true,
	"WORK_KIND_RANK_WORKS":       true,
	"WORK_KIND_EXPLAIN_OWN_WORK": true,
	"output":                     true,
}

func validateAccess(access *pb.Access) error {
	if access == nil || access.GetTaskId() == "" || access.GetRunId() == "" || access.GetAttemptId() == "" || access.GetPurpose() == "" || !challengePurposes[access.GetPurpose()] {
		return invalid("invalid_access")
	}
	return nil
}

func assetMetadata(row assetRow) *pb.AssetMetadata {
	result := &pb.AssetMetadata{Id: row.ID, OwnerId: row.Owner, OriginalFilename: row.Filename, MediaType: row.Detected, Bytes: row.Expected, Sha256: hex.EncodeToString(row.ActualHash), State: assetState(row.State), CreatedAt: timestamppb.New(*row.Created)}
	if row.Detected == "" {
		result.MediaType = row.Declared
	}
	if row.Completed != nil {
		result.CompletedAt = timestamppb.New(*row.Completed)
	}
	return result
}
func assetState(value string) pb.AssetState {
	switch value {
	case "staging":
		return pb.AssetState_ASSET_STATE_STAGING
	case "ready":
		return pb.AssetState_ASSET_STATE_READY
	case "failed":
		return pb.AssetState_ASSET_STATE_FAILED
	case "deleted":
		return pb.AssetState_ASSET_STATE_DELETED
	}
	return pb.AssetState_ASSET_STATE_UNSPECIFIED
}
func sessionState(value string) pb.UploadSessionState {
	switch value {
	case "active":
		return pb.UploadSessionState_UPLOAD_SESSION_STATE_ACTIVE
	case "completed":
		return pb.UploadSessionState_UPLOAD_SESSION_STATE_COMPLETED
	case "aborted":
		return pb.UploadSessionState_UPLOAD_SESSION_STATE_ABORTED
	case "expired":
		return pb.UploadSessionState_UPLOAD_SESSION_STATE_EXPIRED
	}
	return pb.UploadSessionState_UPLOAD_SESSION_STATE_UNSPECIFIED
}

const assetSelect = `SELECT a.id,a.owner_id,a.run_id,a.attempt_id,a.upload_id,a.state,a.original_filename,a.declared_media_type,a.detected_media_type,a.expected_bytes,a.received_bytes,a.expected_sha256,a.actual_sha256,a.staging_key,a.final_key,a.created_at,a.completed_at`

func (s *Server) CreateUploadSession(ctx context.Context, r *pb.CreateUploadSessionRequest) (*pb.CreateUploadSessionResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_CreateUploadSession_FullMethodName)
	if err != nil {
		return nil, err
	}
	if err = storageReady(s); err != nil {
		return nil, err
	}
	if s.UploadSessions == nil {
		return nil, status.Error(codes.Unavailable, "asset_upload_unavailable")
	}
	asset, session, err := s.UploadSessions.Create(ctx, assetapp.CreateSessionCommand{
		Owner: owner, IdempotencyKey: r.GetIdempotencyKey(), Filename: r.GetOriginalFilename(),
		Declared: r.GetDeclaredMediaType(), ExpectedBytes: r.GetExpectedBytes(), ExpectedSHA256: r.GetExpectedSha256(),
	})
	if err != nil {
		return nil, mapUploadError(err)
	}
	row := appAssetRow(asset)
	return &pb.CreateUploadSessionResponse{Asset: assetMetadata(row), UploadSession: &pb.UploadSession{
		Id: session.ID, AssetId: session.AssetID, ReceivedBytes: session.Received, ExpectedBytes: session.Expected,
		State: sessionState(session.State), ExpiresAt: timestamppb.New(session.ExpiresAt),
	}}, nil
}

func appAssetRow(value assetapp.Asset) assetRow {
	return assetRow{ID: value.ID, Owner: value.Owner, RunID: value.RunID, AttemptID: value.AttemptID, UploadID: value.UploadID, State: value.State, Filename: value.Filename, Declared: value.Declared, Detected: value.Detected, StagingKey: value.StagingKey, FinalKey: value.FinalKey, Expected: value.Expected, Received: value.Received, ExpectedHash: value.ExpectedHash, ActualHash: value.ActualHash, Created: value.Created, Completed: value.Completed}
}

func ptrTime(value time.Time) *time.Time { return &value }
func bytesEqual(a, b []byte) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare(a, b) == 1
}
func (s *Server) UploadAsset(stream pb.AssetService_UploadAssetServer) error {
	// Bound large-message buffering before Recv, across all peer connections.
	s.uploadOnce.Do(func() { s.uploadSlots = make(chan struct{}, 2) })
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		return status.Error(codes.ResourceExhausted, "upload_capacity_exhausted")
	}
	first, err := stream.Recv()
	if err != nil {
		if contextErr := platform.ContextError(err); contextErr != nil {
			return contextErr
		}
		return invalid("upload_metadata_required")
	}
	if first.GetActorAssertion() != "" || first.GetUploadSessionId() != "" {
		return s.uploadBrowser(stream, first)
	}
	return s.uploadChallenge(stream, first)
}

func (s *Server) uploadBrowser(stream pb.AssetService_UploadAssetServer, first *pb.UploadAssetRequest) error {
	if err := requireAssetCaller(stream.Context(), "gateway"); err != nil {
		return err
	}
	if !uploadPattern.MatchString(first.GetUploadSessionId()) || !attemptPattern.MatchString(first.GetUploadAttemptId()) || first.GetOffset() < 0 || first.GetActorAssertion() == "" {
		return invalid("invalid_upload_start")
	}
	owner, err := s.actor(stream.Context(), first.GetActorAssertion(), pb.AssetService_UploadAsset_FullMethodName)
	if err != nil {
		return err
	}
	if err = storageReady(s); err != nil {
		return err
	}
	if s.BrowserUpload == nil {
		return status.Error(codes.Unavailable, "asset_upload_unavailable")
	}
	var next int64 = first.GetOffset()
	for {
		message, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return stream.SendAndClose(&pb.UploadAssetResponse{UploadSessionId: first.GetUploadSessionId(), ReceivedBytes: next, NextOffset: next, State: pb.UploadSessionState_UPLOAD_SESSION_STATE_ACTIVE})
		}
		if recvErr != nil {
			return unavailable(recvErr)
		}
		if len(message.GetChunk()) == 0 || len(message.GetChunk()) > maxChunkBytes || message.GetOffset() != next {
			return invalid("invalid_upload_chunk")
		}
		if message.GetChunkSha256() != "" {
			sum := sha256.Sum256(message.GetChunk())
			if !strings.EqualFold(hex.EncodeToString(sum[:]), message.GetChunkSha256()) {
				return status.Error(codes.FailedPrecondition, "chunk_checksum_mismatch")
			}
		}
		next, err = s.BrowserUpload.WriteChunk(stream.Context(), owner, first.GetUploadSessionId(), next, message.GetChunk())
		if err != nil {
			return mapUploadError(err)
		}
	}
}

var serviceUploadPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type challengeStreamFrames struct {
	stream pb.AssetService_UploadAssetServer
}

func (s challengeStreamFrames) Recv() (assetapp.ChallengeFrame, error) {
	message, err := s.stream.Recv()
	if err != nil {
		return assetapp.ChallengeFrame{}, err
	}
	return assetapp.ChallengeFrame{HasMetadata: message.GetAccess() != nil || message.GetUploadId() != "" || message.GetFilename() != "" || message.GetMediaType() != "" || message.GetBytes() != 0 || message.GetSha256() != "", Chunk: message.GetChunk()}, nil
}

func (s *Server) uploadChallenge(stream pb.AssetService_UploadAssetServer, first *pb.UploadAssetRequest) error {
	if err := requireAssetCaller(stream.Context(), "challenge"); err != nil {
		return err
	}
	if err := storageReady(s); err != nil {
		return err
	}
	if err := validateAccess(first.GetAccess()); err != nil || first.GetAccess().GetPurpose() != "WORK_KIND_EXECUTE_PACKAGE" || !serviceUploadPattern.MatchString(first.GetUploadId()) {
		return invalid("invalid_upload")
	}
	if first.GetBytes() < 1 || first.GetBytes() > maxAssetBytes {
		return invalid("invalid_upload")
	}
	expectedHash, err := assetapp.ParseChallengeSHA256(first.GetSha256())
	if err != nil {
		return mapUploadError(err)
	}
	if s.ChallengeUpload == nil {
		return status.Error(codes.Unavailable, "asset_upload_unavailable")
	}
	access := first.GetAccess()
	value, err := s.ChallengeUpload.Execute(stream.Context(), assetapp.ChallengeUpload{
		RunID: access.GetRunId(), AttemptID: access.GetAttemptId(), UploadID: first.GetUploadId(),
		Filename: first.GetFilename(), Declared: first.GetMediaType(), Expected: first.GetBytes(), ExpectedHash: expectedHash,
	}, first.GetChunk(), challengeStreamFrames{stream: stream})
	if err != nil {
		return mapUploadError(err)
	}
	return stream.SendAndClose(&pb.UploadAssetResponse{Asset: assetContract(appAssetRow(value))})
}

func (s *Server) CompleteUpload(ctx context.Context, r *pb.CompleteUploadRequest) (*pb.CompleteUploadResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_CompleteUpload_FullMethodName)
	if err != nil {
		return nil, err
	}
	if err = storageReady(s); err != nil {
		return nil, err
	}
	if !uploadPattern.MatchString(r.GetUploadSessionId()) {
		return nil, status.Error(codes.NotFound, "upload_session_not_found")
	}
	if r.GetAssetId() != "" && !assetPattern.MatchString(r.GetAssetId()) {
		return nil, status.Error(codes.NotFound, "asset_not_found")
	}
	if s.CompleteUseCase == nil {
		return nil, status.Error(codes.Unavailable, "asset_upload_unavailable")
	}
	value, err := s.CompleteUseCase.Execute(ctx, owner, r.GetUploadSessionId(), r.GetAssetId())
	if err != nil {
		return nil, mapUploadError(err)
	}
	return &pb.CompleteUploadResponse{Asset: assetMetadata(appAssetRow(value))}, nil
}

func firstErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func (s *Server) AbortUpload(ctx context.Context, r *pb.AbortUploadRequest) (*pb.AbortUploadResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_AbortUpload_FullMethodName)
	if err != nil {
		return nil, err
	}
	if err = storageReady(s); err != nil {
		return nil, err
	}
	if !uploadPattern.MatchString(r.GetUploadSessionId()) {
		return nil, status.Error(codes.NotFound, "upload_session_not_found")
	}
	if s.AbortUseCase == nil {
		return nil, status.Error(codes.Unavailable, "asset_upload_unavailable")
	}
	session, err := s.AbortUseCase.Execute(ctx, owner, r.GetUploadSessionId())
	if err != nil {
		return nil, mapUploadError(err)
	}
	return &pb.AbortUploadResponse{UploadSession: &pb.UploadSession{Id: session.ID, AssetId: session.AssetID, ReceivedBytes: session.Received, ExpectedBytes: session.Expected, State: sessionState(session.State), ExpiresAt: timestamppb.New(session.ExpiresAt)}}, nil
}

func (s *Server) GetUploadSession(ctx context.Context, r *pb.GetUploadSessionRequest) (*pb.GetUploadSessionResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_GetUploadSession_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !uploadPattern.MatchString(r.GetUploadSessionId()) {
		return nil, status.Error(codes.NotFound, "upload_session_not_found")
	}
	if s.UploadSessions == nil {
		return nil, status.Error(codes.Unavailable, "asset_upload_unavailable")
	}
	session, err := s.UploadSessions.Get(ctx, owner, r.GetUploadSessionId())
	if err != nil {
		return nil, mapUploadError(err)
	}
	return &pb.GetUploadSessionResponse{UploadSession: &pb.UploadSession{Id: session.ID, AssetId: session.AssetID, ReceivedBytes: session.Received, ExpectedBytes: session.Expected, State: sessionState(session.State), ExpiresAt: timestamppb.New(session.ExpiresAt)}}, nil
}

func (s *Server) GetAssetMetadata(ctx context.Context, r *pb.GetAssetMetadataRequest) (*pb.GetAssetMetadataResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_GetAssetMetadata_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !assetPattern.MatchString(r.GetAssetId()) {
		return nil, status.Error(codes.NotFound, "asset_not_found")
	}
	row, err := scanAsset(s.DB.QueryRow(ctx, assetSelect+` FROM asset.assets a WHERE id=$1 AND owner_id=$2`, r.AssetId, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "asset_not_found")
	}
	if err != nil {
		return nil, unavailable(err)
	}
	return &pb.GetAssetMetadataResponse{Asset: assetMetadata(row)}, nil
}

type assetCursor struct {
	V                  int `json:"v"`
	Scope, Created, ID string
}

func assetScope(owner string) string {
	sum := sha256.Sum256([]byte(owner + "|created_at_desc_id_desc"))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func encodeAssetCursor(owner string, created time.Time, id string) string {
	data, _ := json.Marshal(assetCursor{1, assetScope(owner), created.UTC().Format(time.RFC3339Nano), id})
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeAssetCursor(raw, owner string) (time.Time, string, error) {
	if raw == "" {
		return time.Time{}, "", nil
	}
	if len(raw) > maxAssetCursor {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	var c assetCursor
	if json.Unmarshal(data, &c) != nil || c.V != 1 || c.Scope != assetScope(owner) || !assetPattern.MatchString(c.ID) {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, c.Created)
	if err != nil || c.Created != t.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	return t, c.ID, nil
}
func (s *Server) ListMyAssets(ctx context.Context, r *pb.ListMyAssetsRequest) (*pb.ListMyAssetsResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_ListMyAssets_FullMethodName)
	if err != nil {
		return nil, err
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > assetPageLimit {
		return nil, invalid("invalid_request")
	}
	created, cursorID, err := decodeAssetCursor(r.Cursor, owner)
	if err != nil {
		return nil, err
	}
	query := assetSelect + ` FROM asset.assets a WHERE owner_id=$1 AND state='ready'`
	args := []any{owner}
	if r.Cursor != "" {
		args = append(args, created, cursorID)
		query += fmt.Sprintf(` AND (created_at,id)<($%d,$%d)`, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	result := &pb.ListMyAssetsResponse{Items: make([]*pb.AssetMetadata, 0, limit)}
	var last time.Time
	var lastID string
	more := false
	for rows.Next() {
		row, scanErr := scanAsset(rows)
		if scanErr != nil {
			return nil, unavailable(scanErr)
		}
		if len(result.Items) == int(limit) {
			more = true
			break
		}
		result.Items = append(result.Items, assetMetadata(row))
		last = *row.Created
		lastID = row.ID
	}
	if err = rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	if more {
		result.NextCursor = encodeAssetCursor(owner, last, lastID)
	}
	return result, nil
}

func (s *Server) CheckAssetOwnership(ctx context.Context, r *pb.CheckAssetOwnershipRequest) (*pb.CheckAssetOwnershipResponse, error) {
	owner, err := s.actor(ctx, r.GetActorAssertion(), pb.AssetService_CheckAssetOwnership_FullMethodName)
	if err != nil {
		return nil, err
	}
	if len(r.AssetIds) == 0 || len(r.AssetIds) > 32 {
		return nil, invalid("invalid_asset_ids")
	}
	seen := map[string]bool{}
	for _, id := range r.AssetIds {
		if !assetPattern.MatchString(id) || seen[id] {
			return nil, invalid("invalid_asset_ids")
		}
		seen[id] = true
	}
	rows, err := s.DB.Query(ctx, assetSelect+` FROM asset.assets a WHERE owner_id=$1 AND state='ready' AND id=ANY($2)`, owner, r.AssetIds)
	if err != nil {
		return nil, unavailable(err)
	}
	defer rows.Close()
	items := make(map[string]*pb.AssetMetadata)
	for rows.Next() {
		row, scanErr := scanAsset(rows)
		if scanErr != nil {
			return nil, unavailable(scanErr)
		}
		items[row.ID] = assetMetadata(row)
	}
	if err = rows.Err(); err != nil {
		return nil, unavailable(err)
	}
	result := &pb.CheckAssetOwnershipResponse{Assets: make([]*pb.AssetMetadata, 0, len(r.AssetIds))}
	for _, id := range r.AssetIds {
		item := items[id]
		if item == nil {
			return nil, status.Error(codes.FailedPrecondition, "asset_not_ready")
		}
		result.Assets = append(result.Assets, item)
	}
	return result, nil
}

func (s *Server) GetAsset(ctx context.Context, r *pb.GetAssetRequest) (*pb.GetAssetResponse, error) {
	if err := assetPeer(ctx, pb.AssetService_GetAsset_FullMethodName); err != nil {
		return nil, err
	}
	if err := validateAccess(r.GetAccess()); err != nil || !assetPattern.MatchString(r.GetAssetId()) {
		return nil, status.Error(codes.NotFound, "asset_not_found")
	}
	row, err := scanAsset(s.DB.QueryRow(ctx, assetSelect+` FROM asset.assets a WHERE id=$1 AND state='ready'`, r.GetAssetId()))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.RunID != "" && (row.RunID != r.GetAccess().GetRunId() || row.AttemptID != r.GetAccess().GetAttemptId())) {
		return nil, status.Error(codes.NotFound, "asset_not_found")
	}
	if err != nil {
		return nil, unavailable(err)
	}
	return &pb.GetAssetResponse{Asset: assetContract(row)}, nil
}

func (s *Server) ReadAsset(r *pb.ReadAssetRequest, stream pb.AssetService_ReadAssetServer) error {
	if err := assetPeer(stream.Context(), pb.AssetService_ReadAsset_FullMethodName); err != nil {
		return err
	}
	if err := validateAccess(r.GetAccess()); err != nil || !assetPattern.MatchString(r.GetAssetId()) || r.GetOffset() < 0 {
		return status.Error(codes.NotFound, "asset_not_found")
	}
	row, err := scanAsset(s.DB.QueryRow(stream.Context(), assetSelect+` FROM asset.assets a WHERE id=$1 AND state='ready'`, r.GetAssetId()))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && row.RunID != "" && (row.RunID != r.GetAccess().GetRunId() || row.AttemptID != r.GetAccess().GetAttemptId())) {
		return status.Error(codes.NotFound, "asset_not_found")
	}
	if err != nil {
		return unavailable(err)
	}
	if err := storageReady(s); err != nil {
		return err
	}
	reader, err := s.Storage.Open(stream.Context(), row.FinalKey)
	if err != nil {
		return unavailable(err)
	}
	defer reader.Close()
	if r.GetOffset() > 0 {
		if _, err = io.CopyN(io.Discard, reader, r.GetOffset()); err != nil {
			return status.Error(codes.InvalidArgument, "invalid_offset")
		}
	}
	buf := make([]byte, 64<<10)
	offset := r.GetOffset()
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if err = stream.Send(&pb.ReadAssetResponse{Chunk: append([]byte(nil), buf[:n]...), Offset: offset, Sha256: hex.EncodeToString(row.ActualHash)}); err != nil {
				return unavailable(err)
			}
			offset += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return unavailable(readErr)
		}
	}
}

func (s *Server) DownloadAsset(r *pb.DownloadAssetRequest, stream pb.AssetService_DownloadAssetServer) error {
	owner, err := s.actor(stream.Context(), r.GetActorAssertion(), pb.AssetService_DownloadAsset_FullMethodName)
	if err != nil {
		return err
	}
	if err = storageReady(s); err != nil {
		return err
	}
	if !assetPattern.MatchString(r.GetAssetId()) {
		return status.Error(codes.NotFound, "asset_not_found")
	}
	row, err := scanAsset(s.DB.QueryRow(stream.Context(), assetSelect+` FROM asset.assets a WHERE id=$1 AND owner_id=$2 AND state='ready'`, r.AssetId, owner))
	if errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "asset_not_found")
	}
	if err != nil {
		return unavailable(err)
	}
	reader, err := s.Storage.Open(stream.Context(), row.FinalKey)
	if err != nil {
		return unavailable(err)
	}
	defer reader.Close()
	buf := make([]byte, 64<<10)
	var offset int64
	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if err = stream.Send(&pb.DownloadAssetResponse{Offset: offset, Data: data}); err != nil {
				return unavailable(err)
			}
			offset += int64(n)
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return unavailable(readErr)
		}
	}
}
