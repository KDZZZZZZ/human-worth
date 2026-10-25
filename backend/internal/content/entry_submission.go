package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reuse the accepted EntryInput rules without inventing another file/model
// validator. The synthetic wrapper is never persisted as a task.
func entryBundle(e *pb.EntryInput) *pb.TaskDraftInput {
	empty := ""
	return &pb.TaskDraftInput{Title: "entry", Summary: &empty, Description: &empty, Entries: []*pb.EntryInput{e}}
}
func validateEntry(e *pb.EntryInput) ([]byte, error) {
	raw, err := validate(entryBundle(e))
	if err != nil {
		return nil, err
	}
	var wrapper struct {
		Entries []json.RawMessage `json:"entries"`
	}
	if json.Unmarshal(raw, &wrapper) != nil || len(wrapper.Entries) != 1 {
		return nil, invalid("invalid_entry")
	}
	return wrapper.Entries[0], nil
}

const entrySubmissionColumns = `s.id,s.task_id,s.author_id,s.revision,s.origin,s.state,s.content,s.review_id,s.rejection_reason,s.created_at,s.updated_at,s.approved_at`

func scanEntrySubmission(row rowScanner, extra ...any) (*pb.EntrySubmission, error) {
	out := &pb.EntrySubmission{Content: &pb.EntryInput{}}
	var raw []byte
	var created, updated time.Time
	var approved *time.Time
	args := []any{&out.Id, &out.TaskId, &out.AuthorId, &out.Revision, &out.Origin, &out.State, &raw, &out.ReviewId, &out.RejectionReason, &created, &updated, &approved}
	err := row.Scan(append(args, extra...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "entry_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	if protojson.Unmarshal(raw, out.Content) != nil {
		return nil, status.Error(codes.Internal, "invalid_entry_snapshot")
	}
	out.CreatedAt = timestamppb.New(created)
	out.UpdatedAt = timestamppb.New(updated)
	out.ApprovedAt = publicTime(approved)
	return out, nil
}

// Retry returns that operation's immutable version, with its durable review
// result, not a newer mutable revision. Identity is always revalidated first.
func storedEntryVersion(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, id, author string, revision int64, hash []byte) (*pb.EntrySubmission, error) {
	var raw, old []byte
	var state string
	var reason *string
	err := q.QueryRow(ctx, `SELECT r.snapshot,r.content_hash,r.state,r.reason FROM content.entry_review_requests r JOIN content.entry_submissions s ON s.id=r.entry_id WHERE r.entry_id=$1 AND s.author_id=$2 AND r.submission_revision=$3`, id, author, revision).Scan(&raw, &old, &state, &reason)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(old, hash) {
		return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
	}
	snap := &pb.EntryReviewSnapshot{}
	if protojson.Unmarshal(raw, snap) != nil {
		return nil, status.Error(codes.Internal, "invalid_entry_snapshot")
	}
	snap.Submission.State = state
	snap.Submission.RejectionReason = reason
	return snap.Submission, nil
}
func entryRetryError(err error) error {
	if status.Code(err) != codes.Unknown {
		return err
	}
	return storage(err)
}
func (s *Server) GetMyEntrySubmission(ctx context.Context, r *pb.GetMyEntrySubmissionRequest) (*pb.GetMyEntrySubmissionResponse, error) {
	author, err := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_GetMyEntrySubmission_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !reviewedEntryPattern.MatchString(r.GetEntryId()) {
		return nil, status.Error(codes.NotFound, "entry_not_found")
	}
	out, err := scanEntrySubmission(s.DB.QueryRow(ctx, `SELECT `+entrySubmissionColumns+` FROM content.entry_submissions s WHERE s.id=$1 AND s.author_id=$2 AND s.origin='additional'`, r.EntryId, author))
	if err != nil {
		return nil, err
	}
	return &pb.GetMyEntrySubmissionResponse{Submission: out}, nil
}
func lockEntryParent(ctx context.Context, tx pgx.Tx, task string) error {
	var state string
	err := tx.QueryRow(ctx, `SELECT state FROM content.reviewed_tasks WHERE task_id=$1 FOR UPDATE`, task).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return storage(err)
	}
	if state != "published" {
		return status.Error(codes.FailedPrecondition, "task_not_accepting_entries")
	}
	return nil
}
func persistEntryReview(ctx context.Context, tx pgx.Tx, sub *pb.EntrySubmission, hash []byte) error {
	snap := &pb.EntryReviewSnapshot{ReviewId: sub.ReviewId, Submission: sub, CreatedAt: sub.UpdatedAt}
	wire, err := (proto.MarshalOptions{Deterministic: true}).Marshal(snap)
	if err != nil {
		return status.Error(codes.Internal, "internal_error")
	}
	digest := sha256.Sum256(wire)
	raw, err := protojson.Marshal(snap)
	if err != nil {
		return status.Error(codes.Internal, "internal_error")
	}
	_, err = tx.Exec(ctx, `INSERT INTO content.entry_review_requests(id,entry_id,submission_revision,content_hash,snapshot,snapshot_hash,state,created_at) VALUES($1,$2,$3,$4,$5,$6,'pending_review',$7)`, sub.ReviewId, sub.Id, sub.Revision, hash, raw, fmt.Sprintf("%x", digest), sub.UpdatedAt.AsTime())
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO content.entry_review_outbox(id) VALUES($1)`, sub.ReviewId)
	}
	if err != nil {
		return storage(err)
	}
	return nil
}
func (s *Server) SubmitAdditionalEntry(ctx context.Context, r *pb.SubmitAdditionalEntryRequest) (*pb.SubmitAdditionalEntryResponse, error) {
	author, err := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_SubmitAdditionalEntry_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !taskPattern.MatchString(r.GetTaskId()) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if !keyPattern.MatchString(r.GetIdempotencyKey()) {
		return nil, invalid("invalid_idempotency_key")
	}
	if err := workBodyRequired(r.Content); err != nil {
		return nil, err
	}
	data, err := validateEntry(r.Content)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	var existing string
	err = s.DB.QueryRow(ctx, `SELECT id FROM content.entry_submissions WHERE author_id=$1 AND task_id=$2 AND create_key=$3`, author, r.TaskId, r.IdempotencyKey).Scan(&existing)
	if err == nil {
		// Creation idempotency refers to the original accepted submission, not
		// the mutable current revision. A later rejection/resubmission must not
		// make a retry of the original POST return different content or state.
		old, e := storedEntryVersion(ctx, s.DB, existing, author, 1, hash[:])
		return &pb.SubmitAdditionalEntryResponse{Submission: old}, entryRetryErrorOrNil(e)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	if err = s.validateAssets(ctx, author, entryBundle(r.Content), r.AssetActorAssertion); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	// Same parent lock as visibility tightening and every position allocation.
	if err = lockEntryParent(ctx, tx, r.TaskId); err != nil {
		return nil, err
	}
	id, err := reviewID("ent")
	if err != nil {
		return nil, err
	}
	review, err := reviewID("rev")
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO content.entry_submissions(id,task_id,author_id,create_key,create_hash,revision,state,content,review_id) VALUES($1,$2,$3,$4,$5,1,'pending_review',$6,$7) ON CONFLICT(author_id,task_id,create_key) DO NOTHING`, id, r.TaskId, author, r.IdempotencyKey, hash[:], data, review)
	if err != nil {
		return nil, storage(err)
	}
	out, err := scanEntrySubmission(tx.QueryRow(ctx, `SELECT `+entrySubmissionColumns+` FROM content.entry_submissions s WHERE author_id=$1 AND task_id=$2 AND create_key=$3`, author, r.TaskId, r.IdempotencyKey))
	if err != nil {
		return nil, err
	}
	if out.Id != id {
		old, e := storedEntryVersion(ctx, tx, out.Id, author, 1, hash[:])
		return &pb.SubmitAdditionalEntryResponse{Submission: old}, entryRetryErrorOrNil(e)
	}
	if err = persistEntryReview(ctx, tx, out, hash[:]); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.SubmitAdditionalEntryResponse{Submission: out}, nil
}
func entryRetryErrorOrNil(err error) error {
	if err == nil {
		return nil
	}
	return entryRetryError(err)
}
func (s *Server) ResubmitRejectedEntry(ctx context.Context, r *pb.ResubmitRejectedEntryRequest) (*pb.ResubmitRejectedEntryResponse, error) {
	author, err := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_ResubmitRejectedEntry_FullMethodName)
	if err != nil {
		return nil, err
	}
	if !reviewedEntryPattern.MatchString(r.GetEntryId()) {
		return nil, status.Error(codes.NotFound, "entry_not_found")
	}
	if r.ExpectedRevision < 1 || r.ExpectedRevision == int64(^uint64(0)>>1) {
		return nil, invalid("invalid_revision")
	}
	if err := workBodyRequired(r.Content); err != nil {
		return nil, err
	}
	data, err := validateEntry(r.Content)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	if old, e := storedEntryVersion(ctx, s.DB, r.EntryId, author, r.ExpectedRevision+1, hash[:]); e == nil {
		return &pb.ResubmitRejectedEntryResponse{Submission: old}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, entryRetryError(e)
	}
	before, err := scanEntrySubmission(s.DB.QueryRow(ctx, `SELECT `+entrySubmissionColumns+` FROM content.entry_submissions s WHERE s.id=$1 AND s.author_id=$2 AND s.origin='additional'`, r.EntryId, author))
	if err != nil {
		return nil, err
	}
	if before.State != "rejected" || before.Revision != r.ExpectedRevision {
		// Another resubmission may have committed between the version lookup
		// and the current-row read. Recover that exact operation before treating
		// the now-advanced mutable row as a revision conflict.
		if old, e := storedEntryVersion(ctx, s.DB, r.EntryId, author, r.ExpectedRevision+1, hash[:]); e == nil {
			return &pb.ResubmitRejectedEntryResponse{Submission: old}, nil
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return nil, entryRetryError(e)
		}
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	if err = s.validateAssets(ctx, author, entryBundle(r.Content), r.AssetActorAssertion); err != nil {
		return nil, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	if err = lockEntryParent(ctx, tx, before.TaskId); err != nil {
		return nil, err
	}
	current, err := scanEntrySubmission(tx.QueryRow(ctx, `SELECT `+entrySubmissionColumns+` FROM content.entry_submissions s WHERE s.id=$1 AND s.author_id=$2 AND s.origin='additional' FOR UPDATE`, r.EntryId, author))
	if err != nil {
		return nil, err
	}
	if old, e := storedEntryVersion(ctx, tx, r.EntryId, author, r.ExpectedRevision+1, hash[:]); e == nil {
		return &pb.ResubmitRejectedEntryResponse{Submission: old}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, entryRetryError(e)
	}
	if current.State != "rejected" || current.Revision != r.ExpectedRevision {
		return nil, status.Error(codes.Aborted, "revision_conflict")
	}
	review, err := reviewID("rev")
	if err != nil {
		return nil, err
	}
	out, err := scanEntrySubmission(tx.QueryRow(ctx, `UPDATE content.entry_submissions s SET content=$3,revision=revision+1,state='pending_review',review_id=$4,rejection_reason=NULL,updated_at=clock_timestamp() WHERE id=$1 AND author_id=$2 RETURNING `+entrySubmissionColumns, r.EntryId, author, data, review))
	if err != nil {
		return nil, err
	}
	if err = persistEntryReview(ctx, tx, out, hash[:]); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.ResubmitRejectedEntryResponse{Submission: out}, nil
}

type ownEntryCursor struct {
	Version int    `json:"v"`
	Scope   string `json:"scope"`
	Created string `json:"createdAt"`
	ID      string `json:"id"`
}

func ownEntryScope(author, state string, category *string) string {
	raw, _ := json.Marshal(struct {
		Author, State, Kind, Sort string
		Category                  *string
	}{author, state, "entry", submissionSort, category})
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
func ownEntryNext(scope string, created time.Time, id string) string {
	raw, _ := json.Marshal(ownEntryCursor{1, scope, created.UTC().Format(time.RFC3339Nano), id})
	return base64.RawURLEncoding.EncodeToString(raw)
}
func decodeOwnEntryCursor(raw, scope string) (time.Time, string, error) {
	if len(raw) > maxSubmissionCursorLength {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	var c ownEntryCursor
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Scope != scope || !reviewedEntryPattern.MatchString(c.ID) {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, c.Created)
	if err != nil || raw != ownEntryNext(scope, created, c.ID) {
		return time.Time{}, "", invalid("invalid_cursor")
	}
	return created, c.ID, nil
}
func (s *Server) ListMyEntrySubmissions(ctx context.Context, r *pb.ListMyEntrySubmissionsRequest) (*pb.ListMyEntrySubmissionsResponse, error) {
	author, err := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_ListMyEntrySubmissions_FullMethodName)
	if err != nil {
		return nil, err
	}
	switch r.State {
	case "", "pending_review", "approved", "rejected", "approval_revoked":
	default:
		return nil, invalid("unsupported_submission_filter")
	}
	if r.Category != nil && (*r.Category == "" || !validCategory(*r.Category)) {
		return nil, invalid("invalid_category")
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("invalid_request")
	}
	scope := ownEntryScope(author, r.State, r.Category)
	query := `SELECT ` + entrySubmissionColumns + ` FROM content.entry_submissions s JOIN content.reviewed_tasks t ON t.task_id=s.task_id WHERE s.author_id=$1 AND s.origin='additional'`
	args := []any{author}
	if r.State != "" {
		args = append(args, r.State)
		query += fmt.Sprintf(` AND s.state=$%d`, len(args))
	}
	if r.Category != nil {
		args = append(args, *r.Category)
		query += fmt.Sprintf(` AND t.snapshot->>'category'=$%d`, len(args))
	}
	if r.Cursor != "" {
		created, id, e := decodeOwnEntryCursor(r.Cursor, scope)
		if e != nil {
			return nil, e
		}
		args = append(args, created, id)
		query += fmt.Sprintf(` AND (s.created_at,s.id)<($%d,$%d)`, len(args)-1, len(args))
	}
	args = append(args, limit+1)
	query += fmt.Sprintf(` ORDER BY s.created_at DESC,s.id DESC LIMIT $%d`, len(args))
	rows, err := s.DB.Query(ctx, query, args...)
	if err != nil {
		return nil, storage(err)
	}
	defer rows.Close()
	out := &pb.ListMyEntrySubmissionsResponse{Items: make([]*pb.EntrySubmission, 0, limit)}
	size := 0
	for rows.Next() {
		if len(out.Items) == int(limit) {
			return out, nil
		}
		item, e := scanEntrySubmission(rows)
		if e != nil {
			return nil, e
		}
		next := ownEntryNext(scope, item.CreatedAt.AsTime(), item.Id)
		n := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(item))
		if size+n+protowire.SizeTag(2)+protowire.SizeBytes(len(next)) > maxSubmissionPageBytes {
			if len(out.Items) == 0 {
				return nil, status.Error(codes.Internal, "stored_submission_too_large")
			}
			return out, nil
		}
		out.Items = append(out.Items, item)
		out.NextCursor = next
		size += n
	}
	if err = rows.Err(); err != nil {
		return nil, storage(err)
	}
	out.NextCursor = ""
	return out, nil
}
