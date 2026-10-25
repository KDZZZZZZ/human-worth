package content

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Anonymous HTTP still requires both an authenticated gateway peer and an
// Identity-issued, method-bound assertion. Supplied credentials never downgrade.
func (s *Server) publicActor(ctx context.Context, assertion, method string) error {
	if err := gatewayPeer(ctx); err != nil {
		return err
	}
	if s.Identity == nil {
		return status.Error(codes.Unavailable, "identity_unavailable")
	}
	verified, err := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return err
	}
	switch verified.GetPrincipal().GetClientKind() {
	case identitypb.ClientKind_CLIENT_KIND_ANONYMOUS, identitypb.ClientKind_CLIENT_KIND_WEB_SESSION, identitypb.ClientKind_CLIENT_KIND_MCP_READ:
		return nil
	}
	return status.Error(codes.PermissionDenied, "credential_purpose_forbidden")
}
func publicTime(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
func (s *Server) GetTask(ctx context.Context, r *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	if err := s.publicActor(ctx, r.GetActorAssertion(), pb.ContentService_GetTask_FullMethodName); err != nil {
		return nil, err
	}
	if !taskPattern.MatchString(r.GetTaskId()) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	out := &pb.PublicTask{Author: &pb.PublicAuthor{}}
	var published *time.Time
	err := s.DB.QueryRow(ctx, `SELECT t.task_id,t.source_revision,t.snapshot->>'title',t.snapshot->>'summary',t.snapshot->>'description',t.snapshot->>'category',d.author_id,t.published_at
 FROM content.reviewed_tasks t JOIN content.task_drafts d ON d.id=t.task_id WHERE t.task_id=$1 AND t.state='published' AND `+catalogPublic+``, r.TaskId).Scan(&out.Id, &out.Revision, &out.Title, &out.Summary, &out.Description, &out.Category, &out.Author.Id, &published)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	out.PublishedAt = publicTime(published)
	attachments, err := taskAttachments(ctx, s.DB, r.TaskId)
	if err != nil {
		return nil, err
	}
	for _, a := range attachments {
		out.TaskAttachments = append(out.TaskAttachments, a.Asset)
	}
	return &pb.GetTaskResponse{Task: out}, nil
}

// One shared frozen projection for browse and authorization. Initial rows keep
// their exact legacy array reference; additional rows carry an immutable copy.
const publicEntryContent = `COALESCE(e.content,t.snapshot->'entries'->e.position)`

const publicEntrySort = "snapshot_position_asc_id_asc"

type entryCursor struct {
	Version        int    `json:"v"`
	Task           string `json:"task"`
	SourceRevision int64  `json:"sourceRevision"`
	Sort           string `json:"sort"`
	Position       int32  `json:"position"`
	ID             string `json:"id"`
}

func encodeEntryCursor(task string, revision int64, position int32, id string) string {
	version := 1
	if position > 31 {
		version = 2
	}
	data, _ := json.Marshal(entryCursor{Version: version, Task: task, SourceRevision: revision, Sort: publicEntrySort, Position: position, ID: id})
	return base64.RawURLEncoding.EncodeToString(data)
}
func decodeEntryCursor(raw, task string) (entryCursor, error) {
	var c entryCursor
	if raw == "" {
		c.Position = -1
		return c, nil
	}
	if len(raw) > maxSubmissionCursorLength {
		return c, invalid("invalid_cursor")
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(data) > 1024 {
		return c, invalid("invalid_cursor")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || (c.Version != 1 && c.Version != 2) || (c.Version == 1 && c.Position > 31) || (c.Version == 2 && c.Position <= 31) || c.Task != task || c.Sort != publicEntrySort || c.SourceRevision < 1 || c.Position < 0 || !reviewedEntryPattern.MatchString(c.ID) {
		return c, invalid("invalid_cursor")
	}
	if raw != encodeEntryCursor(c.Task, c.SourceRevision, c.Position, c.ID) {
		return c, invalid("invalid_cursor")
	}
	return c, nil
}
func (s *Server) ListApprovedEntries(ctx context.Context, r *pb.ListApprovedEntriesRequest) (*pb.ListApprovedEntriesResponse, error) {
	if err := s.publicActor(ctx, r.GetActorAssertion(), pb.ContentService_ListApprovedEntries_FullMethodName); err != nil {
		return nil, err
	}
	if !taskPattern.MatchString(r.GetTaskId()) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("invalid_request")
	}
	cursor, err := decodeEntryCursor(r.Cursor, r.TaskId)
	if err != nil {
		return nil, err
	}
	// ONE statement checks current task/entry eligibility and fetches limit+1.
	// LEFT JOIN preserves the visible parent even when no approved entries remain.
	// No cross-service calls or transaction spanning RPCs. Existing task/position
	// unique index supports the growing ordered range. Each page is byte-bounded.
	rows, err := s.DB.Query(ctx, `SELECT t.source_revision,COALESCE(e.author_id,d.author_id),e.id,e.position,e.approved_at,
 (`+publicEntryContent+`)-'agentConfiguration'-'permissions'
 FROM content.reviewed_tasks t JOIN content.task_drafts d ON d.id=t.task_id
 LEFT JOIN LATERAL (SELECT id,position,approved_at,author_id,content FROM content.reviewed_entries
 WHERE task_id=t.task_id AND state='approved' AND (position,id)>($2,$3)
 ORDER BY position,id LIMIT $4) e ON true
 WHERE t.task_id=$1 AND t.state='published' AND `+catalogPublic+` ORDER BY e.position,e.id`, r.TaskId, cursor.Position, cursor.ID, limit+1)
	if err != nil {
		return nil, storage(err)
	}
	defer rows.Close()
	out := &pb.ListApprovedEntriesResponse{Items: make([]*pb.ApprovedEntry, 0, limit)}
	found := false
	var lastPosition int32
	var sourceRevision int64
	itemsSize := 0
	for rows.Next() {
		found = true
		var author string
		var id *string
		var position *int32
		var approved *time.Time
		var raw []byte
		if err = rows.Scan(&sourceRevision, &author, &id, &position, &approved, &raw); err != nil {
			return nil, storage(err)
		}
		if r.Cursor != "" && cursor.SourceRevision != sourceRevision {
			return nil, invalid("invalid_cursor")
		}
		if id == nil {
			continue
		}
		if len(out.Items) == int(limit) {
			out.NextCursor = encodeEntryCursor(r.TaskId, sourceRevision, lastPosition, out.Items[len(out.Items)-1].Id)
			break
		}
		input := &pb.EntryInput{}
		if position == nil || protojson.Unmarshal(raw, input) != nil {
			return nil, status.Error(codes.Internal, "invalid_review_snapshot")
		}
		// Construct an allowlisted type, never forward EntryInput or ReviewSnapshot.
		item := &pb.ApprovedEntry{Id: *id, TaskId: r.TaskId, Side: input.Side, Title: input.Title, Description: input.GetDescription(), Source: input.Source, Artifacts: input.Artifacts, Author: &pb.PublicAuthor{Id: author}, ApprovedAt: publicTime(approved)}
		itemSize := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(item))
		next := encodeEntryCursor(r.TaskId, sourceRevision, *position, *id)
		if itemsSize+itemSize+protowire.SizeTag(2)+protowire.SizeBytes(len(next)) > maxSubmissionPageBytes {
			if len(out.Items) == 0 {
				return nil, status.Error(codes.Internal, "stored_submission_too_large")
			}
			out.NextCursor = encodeEntryCursor(r.TaskId, sourceRevision, lastPosition, out.Items[len(out.Items)-1].Id)
			break
		}
		itemsSize += itemSize
		out.Items = append(out.Items, item)
		lastPosition = *position
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, storage(err)
	}
	if !found {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	return out, nil
}
