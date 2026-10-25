package content

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
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

type Server struct {
	Voting votingpb.VotingServiceClient
	pb.UnimplementedContentServiceServer
	DB       *pgxpool.Pool
	Identity identitypb.IdentityServiceClient
	Asset    assetpb.AssetServiceClient
}

// Service identity and user identity are independent trust boundaries.
func Authorization(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if err := contentPeer(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return next(ctx, request)
}
func contentPeer(ctx context.Context, method string) error {
	switch method {
	case pb.ContentService_GetChallengeTaskDescription_FullMethodName, pb.ContentService_ListChallengeWorks_FullMethodName, pb.ContentService_ListChallengeComments_FullMethodName, pb.ContentService_CheckChallengeMaterials_FullMethodName, pb.ContentService_OpenRunRegistration_FullMethodName, pb.ContentService_CloseRunRegistration_FullMethodName, pb.ContentService_RegisterChallengeCandidate_FullMethodName, pb.ContentService_GetChallengeRegistration_FullMethodName:
		return requireContentCaller(ctx, "challenge")
	case pb.ContentService_ListDiscoverableTasks_FullMethodName:
		return requireContentCaller(ctx, "discovery")
	case pb.ContentService_CheckModerationTarget_FullMethodName, pb.ContentService_ApplyTakedown_FullMethodName:
		return requireContentCaller(ctx, "moderation")
	case pb.ContentService_GetVotingCatalog_FullMethodName, pb.ContentService_GetVotingCatalogSnapshot_FullMethodName:
		return requireContentCaller(ctx, "voting")
	case pb.ContentService_CheckAssetAccess_FullMethodName, pb.ContentService_CheckReviewAssetAccess_FullMethodName:
		return requireContentCaller(ctx, "asset")
	case pb.ContentService_GetEntryReviewSnapshot_FullMethodName, pb.ContentService_ApplyEntryReviewDecision_FullMethodName, pb.ContentService_ApplyAssetReview_FullMethodName, pb.ContentService_GetReviewSnapshot_FullMethodName, pb.ContentService_ApplyReviewDecision_FullMethodName:
		return requireContentCaller(ctx, "moderation")
	case "/grpc.health.v1.Health/Check":
		name, err := platform.ServiceName(ctx)
		if err == nil && (name == "asset" || name == "moderation") {
			return nil
		}
	}
	return gatewayPeer(ctx)
}
func gatewayPeer(ctx context.Context) error {
	name, err := platform.ServiceName(ctx)
	if err != nil {
		return status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if name != "gateway" {
		return status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return nil
}
func AuthorizationStream(service any, stream grpc.ServerStream, _ *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	if err := gatewayPeer(stream.Context()); err != nil {
		return err
	}
	return next(service, stream)
}
func (s *Server) actor(ctx context.Context, assertion, method string) (string, error) {
	// Verify again on every request, including retries: never cache an assertion result.
	if err := gatewayPeer(ctx); err != nil {
		return "", err
	}
	if s.Identity == nil {
		return "", status.Error(codes.Unavailable, "identity_unavailable")
	}
	response, err := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return "", err
	}
	p := response.GetPrincipal()
	if p.GetClientKind() != identitypb.ClientKind_CLIENT_KIND_WEB_SESSION || p.GetAccountId() == "" {
		return "", status.Error(codes.PermissionDenied, "web_session_required")
	}
	return p.AccountId, nil
}

var keyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)
var taskPattern = regexp.MustCompile(`^tsk_[a-f0-9]{32}$`)

const maxDraftBytes = 12000
const maxSubmissionCursorLength = 2048
const maxSubmissionPageBytes = 2 << 20
const submissionSort = "created_at_desc_id_desc"

type submissionFilter struct {
	Kind     string  `json:"kind"`
	State    string  `json:"state"`
	Category *string `json:"category,omitempty"`
}
type submissionCursor struct {
	Version   int    `json:"v"`
	Scope     string `json:"scope"`
	CreatedAt string `json:"createdAt"`
	ID        string `json:"id"`
}

func invalid(reason string) error { return status.Error(codes.InvalidArgument, reason) }
func storage(err error) error {
	if err == nil {
		return nil
	}
	return platform.DependencyError(err, "content_unavailable")
}

func validCategory(value string) bool {
	return utf8.ValidString(value) && len(value) <= maxDraftBytes && !strings.ContainsRune(value, 0)
}

// Validation is repeated at the gRPC boundary, not trusted to the gateway.
func validate(input *pb.TaskDraftInput) ([]byte, error) {
	if input == nil || strings.TrimSpace(input.Title) == "" || input.Summary == nil || input.Description == nil || len(input.Entries) > 32 || (input.Category != nil && !validCategory(input.GetCategory())) {
		return nil, invalid("invalid_draft")
	}
	seenAttachments := map[string]bool{}
	if len(input.TaskAttachments) > 32 {
		return nil, invalid("too_many_assets")
	}
	for _, a := range input.TaskAttachments {
		if a == nil || a.CloudUseAllowed == nil || !reviewedAssetPattern.MatchString(a.AssetId) || seenAttachments[a.AssetId] {
			return nil, invalid("invalid_task_attachment")
		}
		seenAttachments[a.AssetId] = true
	}
	for _, e := range input.Entries {
		if e == nil || (e.Side != "human" && e.Side != "agent") || strings.TrimSpace(e.Title) == "" || strings.TrimSpace(e.Source) == "" || len(e.Artifacts) == 0 || len(e.Artifacts) > 32 || e.Permissions == nil || e.Permissions.CloudUse == nil {
			return nil, invalid("invalid_entry")
		}
		if (e.Side == "agent" && (e.AgentConfiguration == nil || strings.TrimSpace(e.AgentConfiguration.Model) == "")) || (e.Side == "human" && e.AgentConfiguration != nil) {
			return nil, invalid("invalid_agent_configuration")
		}
		for _, a := range e.Artifacts {
			if a == nil {
				return nil, invalid("invalid_artifact")
			}
			// File references are verified against Asset before persistence.
			if a.Kind == "file" {
				if a.AssetId == nil || a.GetAssetId() == "" || a.Url != nil || a.Text != nil {
					return nil, invalid("invalid_artifact")
				}
				continue
			}
			if a.Kind == "text" {
				if a.Text == nil || strings.TrimSpace(a.GetText()) == "" || a.AssetId != nil || a.Url != nil {
					return nil, invalid("invalid_artifact")
				}
				continue
			}
			if a.Kind != "link" || a.Url == nil || a.AssetId != nil || a.Text != nil {
				return nil, invalid("invalid_artifact")
			}
			u, err := url.Parse(a.GetUrl())
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
				return nil, invalid("invalid_artifact_url")
			}
		}
	}
	data, err := (protojson.MarshalOptions{EmitDefaultValues: true}).Marshal(input)
	if err != nil || !utf8.Valid(data) || len(data) > maxDraftBytes {
		return nil, invalid("draft_too_large")
	}
	// encoding/json sorts parameter object keys for a stable semantic create fingerprint.
	var value any
	if err = json.Unmarshal(data, &value); err != nil {
		return nil, invalid("invalid_draft")
	}
	if !validJSONStrings(value) {
		return nil, invalid("invalid_draft")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("invalid_draft")
	}
	if len(canonical) > maxDraftBytes {
		return nil, invalid("draft_too_large")
	}
	return canonical, nil
}

func (s *Server) validateAssets(ctx context.Context, author string, input *pb.TaskDraftInput, assertion string) error {
	ids := make([]string, 0)
	seen := make(map[string]bool)
	for _, entry := range input.GetEntries() {
		for _, artifact := range entry.GetArtifacts() {
			if artifact.GetKind() == "file" && !seen[artifact.GetAssetId()] {
				seen[artifact.GetAssetId()] = true
				ids = append(ids, artifact.GetAssetId())
			}
		}
	}
	for _, a := range input.GetTaskAttachments() {
		if !seen[a.AssetId] {
			seen[a.AssetId] = true
			ids = append(ids, a.AssetId)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if s.Asset == nil || assertion == "" {
		return status.Error(codes.FailedPrecondition, "asset_service_not_ready")
	}
	if len(ids) > 32 {
		return invalid("too_many_assets")
	}
	response, err := s.Asset.CheckAssetOwnership(ctx, &assetpb.CheckAssetOwnershipRequest{ActorAssertion: assertion, AssetIds: ids})
	if err != nil {
		return err
	}
	if len(response.GetAssets()) != len(ids) {
		return status.Error(codes.FailedPrecondition, "asset_not_ready")
	}
	returned := map[string]bool{}
	for _, asset := range response.GetAssets() {
		if !seen[asset.GetId()] || returned[asset.GetId()] {
			return status.Error(codes.FailedPrecondition, "asset_not_ready")
		}
		returned[asset.Id] = true
		if asset.GetOwnerId() != author || asset.GetState() != assetpb.AssetState_ASSET_STATE_READY {
			return status.Error(codes.FailedPrecondition, "asset_not_ready")
		}
	}
	// Immutable metadata is cached only after a fresh owner/readiness check.
	// It grants no access: every public or Challenge read still checks references.
	for _, a := range response.Assets {
		metadata, e := protojson.Marshal(&assetpb.Asset{Id: a.Id, Filename: a.OriginalFilename, MediaType: a.MediaType, Bytes: a.Bytes, Sha256: a.Sha256})
		if e != nil {
			return storage(e)
		}
		if _, e = s.DB.Exec(ctx, `INSERT INTO content.asset_descriptors(asset_id,metadata) VALUES($1,$2) ON CONFLICT(asset_id) DO NOTHING`, a.Id, metadata); e != nil {
			return storage(e)
		}
	}
	return nil
}

func (s *Server) CreateTaskDraft(ctx context.Context, r *pb.CreateTaskDraftRequest) (*pb.CreateTaskDraftResponse, error) {
	author, err := s.actor(ctx, r.ActorAssertion, pb.ContentService_CreateTaskDraft_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !keyPattern.MatchString(r.IdempotencyKey) {
		return nil, invalid("invalid_idempotency_key")
	}
	data, err := validate(r.Content)
	if err != nil {
		return nil, err
	}
	if err = s.validateAssets(ctx, author, r.Content, r.AssetActorAssertion); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, status.Error(codes.Internal, "internal_error")
	}
	id := fmt.Sprintf("tsk_%x", random)
	_, err = tx.Exec(ctx, `INSERT INTO content.task_drafts(id,author_id,create_key,create_hash,content) VALUES($1,$2,$3,$4,$5) ON CONFLICT(author_id,create_key) DO NOTHING`, id, author, r.IdempotencyKey, hash[:], data)
	if err != nil {
		return nil, storage(err)
	}
	submission, original, err := scanTaskSubmission(tx.QueryRow(ctx, `SELECT `+taskSubmissionColumns+` FROM content.task_drafts WHERE author_id=$1 AND create_key=$2`, author, r.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(original, hash[:]) {
		return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	// Replay returns the same task's current snapshot, never rolls back later edits.
	return &pb.CreateTaskDraftResponse{Submission: submission}, nil
}

type rowScanner interface {
	Scan(...any) error
}

// taskSubmissionColumns is the shared projection consumed by scanTaskSubmission.
// Keep query-specific predicates and ordering at each call site, but maintain
// this positional contract in one place.
const taskSubmissionColumns = `id,author_id,revision,state,content,create_hash,review_id,rejection_reason`

const taskSubmissionListColumns = taskSubmissionColumns + `,created_at`

func scanTaskSubmission(row rowScanner, extra ...any) (*pb.TaskSubmission, []byte, error) {
	result := &pb.TaskSubmission{Content: &pb.TaskDraftInput{}}
	var data []byte
	var hash []byte
	values := []any{&result.Id, &result.AuthorId, &result.Revision, &result.State, &data, &hash, &result.ReviewId, &result.RejectionReason}
	values = append(values, extra...)
	err := row.Scan(values...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, nil, storage(err)
	}
	if err = protojson.Unmarshal(data, result.Content); err != nil {
		return nil, nil, status.Error(codes.Internal, "invalid_stored_draft")
	}
	return result, hash, nil
}
func (s *Server) GetMyTaskSubmission(ctx context.Context, r *pb.GetMyTaskSubmissionRequest) (*pb.GetMyTaskSubmissionResponse, error) {
	author, err := s.actor(ctx, r.ActorAssertion, pb.ContentService_GetMyTaskSubmission_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !taskPattern.MatchString(r.TaskId) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	result, _, err := scanTaskSubmission(s.DB.QueryRow(ctx, `SELECT `+taskSubmissionColumns+` FROM content.task_drafts WHERE id=$1 AND author_id=$2`, r.TaskId, author))
	if err != nil {
		return nil, err
	}
	return &pb.GetMyTaskSubmissionResponse{Submission: result}, nil
}
func effectiveSubmissionFilter(r *pb.ListMySubmissionsRequest) (submissionFilter, int32, error) {
	if r == nil {
		return submissionFilter{}, 0, invalid("invalid_request")
	}
	filter := submissionFilter{Kind: r.Kind, State: r.State}
	if filter.Kind == "" {
		filter.Kind = "task"
	}
	if filter.State == "" {
		filter.State = "draft"
	}
	if filter.Kind != "task" || !validSubmissionState(filter.State) {
		return submissionFilter{}, 0, invalid("unsupported_submission_filter")
	}
	if r.Category != nil {
		category := r.GetCategory()
		if category == "" || !validCategory(category) {
			return submissionFilter{}, 0, invalid("invalid_category")
		}
		filter.Category = &category
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return submissionFilter{}, 0, invalid("invalid_request")
	}
	return filter, limit, nil
}
func submissionScope(author string, filter submissionFilter) string {
	data, _ := json.Marshal(struct {
		Author string           `json:"author"`
		Filter submissionFilter `json:"filter"`
		Sort   string           `json:"sort"`
	}{Author: author, Filter: filter, Sort: submissionSort})
	sum := sha256.Sum256(data)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func encodeSubmissionCursor(author string, filter submissionFilter, created time.Time, id string) string {
	data, _ := json.Marshal(submissionCursor{Version: 1, Scope: submissionScope(author, filter), CreatedAt: created.UTC().Format(time.RFC3339Nano), ID: id})
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeSubmissionCursor(raw, author string, filter submissionFilter) (time.Time, string, bool, error) {
	if raw == "" {
		return time.Time{}, "", false, nil
	}
	if len(raw) > maxSubmissionCursorLength {
		return time.Time{}, "", false, invalid("invalid_cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 1024 {
		return time.Time{}, "", false, invalid("invalid_cursor")
	}
	var cursor submissionCursor
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&cursor) != nil || decoder.Decode(new(any)) != io.EOF || cursor.Version != 1 || cursor.Scope != submissionScope(author, filter) || !taskPattern.MatchString(cursor.ID) {
		return time.Time{}, "", false, invalid("invalid_cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, cursor.CreatedAt)
	if err != nil || cursor.CreatedAt != created.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, "", false, invalid("invalid_cursor")
	}
	return created, cursor.ID, true, nil
}
func (s *Server) ListMySubmissions(ctx context.Context, r *pb.ListMySubmissionsRequest) (*pb.ListMySubmissionsResponse, error) {
	author, err := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_ListMySubmissions_FullMethodName)
	if err != nil {
		return nil, err
	}
	filter, limit, err := effectiveSubmissionFilter(r)
	if err != nil {
		return nil, err
	}
	created, cursorID, hasCursor, err := decodeSubmissionCursor(r.Cursor, author, filter)
	if err != nil {
		return nil, err
	}
	query := `SELECT ` + taskSubmissionListColumns + ` FROM content.task_drafts WHERE author_id=$1 AND state=$2`
	args := []any{author, filter.State}
	if filter.Category != nil {
		args = append(args, *filter.Category)
		query += fmt.Sprintf(` AND content->>'category'=$%d`, len(args))
	}
	if hasCursor {
		args = append(args, created, cursorID)
		query += fmt.Sprintf(` AND (created_at,id)<($%d,$%d)`, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY created_at DESC,id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, storage(err)
	}
	defer rows.Close()
	page := newSubmissionPage(author, filter, limit)
	for rows.Next() {
		if len(page.response.Items) == int(limit) {
			return page.finish(true), nil
		}
		var itemCreated time.Time
		item, _, scanErr := scanTaskSubmission(rows, &itemCreated)
		if scanErr != nil {
			return nil, scanErr
		}
		added, addErr := page.add(item, itemCreated)
		if addErr != nil {
			return nil, addErr
		}
		if !added {
			return page.finish(true), nil
		}
	}
	if err = rows.Err(); err != nil {
		return nil, storage(err)
	}
	return page.finish(false), nil
}

type submissionPageBuilder struct {
	response  *pb.ListMySubmissionsResponse
	author    string
	filter    submissionFilter
	itemsSize int
}

func newSubmissionPage(author string, filter submissionFilter, limit int32) *submissionPageBuilder {
	return &submissionPageBuilder{
		response: &pb.ListMySubmissionsResponse{Items: make([]*pb.TaskSubmission, 0, limit)},
		author:   author,
		filter:   filter,
	}
}

func (p *submissionPageBuilder) add(item *pb.TaskSubmission, created time.Time) (bool, error) {
	itemSize := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(item))
	nextCursor := encodeSubmissionCursor(p.author, p.filter, created, item.Id)
	nextSize := protowire.SizeTag(2) + protowire.SizeBytes(len(nextCursor))
	if p.itemsSize+itemSize+nextSize > maxSubmissionPageBytes {
		if len(p.response.Items) == 0 {
			return false, status.Error(codes.Internal, "stored_submission_too_large")
		}
		return false, nil
	}
	p.response.Items = append(p.response.Items, item)
	p.response.NextCursor = nextCursor
	p.itemsSize += itemSize
	return true, nil
}

func (p *submissionPageBuilder) finish(hasMore bool) *pb.ListMySubmissionsResponse {
	if !hasMore {
		p.response.NextCursor = ""
	}
	return p.response
}

func (s *Server) ReplaceTaskDraft(ctx context.Context, r *pb.ReplaceTaskDraftRequest) (*pb.ReplaceTaskDraftResponse, error) {
	author, err := s.actor(ctx, r.ActorAssertion, pb.ContentService_ReplaceTaskDraft_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !taskPattern.MatchString(r.TaskId) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if r.ExpectedRevision < 1 {
		return nil, invalid("invalid_revision")
	}
	data, err := validate(r.Content)
	if err != nil {
		return nil, err
	}
	if err = s.validateAssets(ctx, author, r.Content, r.AssetActorAssertion); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	previous, _, err := scanTaskSubmission(tx.QueryRow(ctx, `SELECT `+taskSubmissionColumns+` FROM content.task_drafts WHERE id=$1 AND author_id=$2 FOR UPDATE`, r.TaskId, author))
	if err != nil {
		return nil, err
	}
	if (previous.State != "draft" && previous.State != "rejected") || previous.Revision != r.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	result, _, err := scanTaskSubmission(tx.QueryRow(ctx, `UPDATE content.task_drafts SET content=$3,revision=revision+1,state='draft',review_id=NULL,rejection_reason=NULL,updated_at=clock_timestamp() WHERE id=$1 AND author_id=$2 RETURNING `+taskSubmissionColumns, r.TaskId, author, data))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.ReplaceTaskDraftResponse{Submission: result}, nil
}

// PostgreSQL jsonb cannot store NUL; reject it as input rather than reporting a
// transient database outage. Parameters remain inert JSON, not executable code.
func validJSONStrings(value any) bool {
	switch value := value.(type) {
	case string:
		return !strings.ContainsRune(value, 0)
	case map[string]any:
		for key, child := range value {
			if strings.ContainsRune(key, 0) || !validJSONStrings(child) {
				return false
			}
		}
	case []any:
		for _, child := range value {
			if !validJSONStrings(child) {
				return false
			}
		}
	}
	return true
}

func validSubmissionState(s string) bool {
	switch s {
	case "draft", "pending_review", "rejected", "published", "taken_down":
		return true
	}
	return false
}
