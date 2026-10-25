package content

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"
	"unicode/utf8"
)

type commentCursor struct {
	Task string    `json:"task"`
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

func scanComment(row rowScanner) (*pb.PublicComment, error) {
	out := &pb.PublicComment{Author: &pb.PublicAuthor{}}
	var created time.Time
	e := row.Scan(&out.Id, &out.TaskId, &out.Author.Id, &out.Body, &created)
	if e != nil {
		return nil, e
	}
	out.CreatedAt = timestamppb.New(created)
	return out, nil
}
func (s *Server) ListComments(ctx context.Context, r *pb.ListCommentsRequest) (*pb.ListCommentsResponse, error) {
	if e := s.publicActor(ctx, r.GetActorAssertion(), pb.ContentService_ListComments_FullMethodName); e != nil {
		return nil, e
	}
	if !taskPattern.MatchString(r.TaskId) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 {
		return nil, invalid("invalid_request")
	}
	c := commentCursor{Task: r.TaskId, Time: time.Unix(0, 0)}
	if r.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(r.Cursor)
		if e != nil || len(b) > 1024 || json.Unmarshal(b, &c) != nil || c.Task != r.TaskId || c.ID == "" || c.Time.IsZero() {
			return nil, invalid("invalid_cursor")
		}
	}
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, storage(e)
	}
	defer tx.Rollback(ctx)
	var visible bool
	e = tx.QueryRow(ctx, `SELECT state='published' AND `+catalogPublic+` FROM content.reviewed_tasks t WHERE task_id=$1`, r.TaskId).Scan(&visible)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !visible {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if e != nil {
		return nil, storage(e)
	}
	rows, e := tx.Query(ctx, `SELECT id,task_id,author_id,body,created_at FROM content.comments WHERE task_id=$1 AND state='visible' AND (created_at,id)>($2,$3) ORDER BY created_at,id LIMIT $4`, r.TaskId, c.Time, c.ID, limit+1)
	if e != nil {
		return nil, storage(e)
	}
	defer rows.Close()
	out := &pb.ListCommentsResponse{}
	for rows.Next() {
		v, e := scanComment(rows)
		if e != nil {
			return nil, storage(e)
		}
		if len(out.Items) == int(limit) {
			last := out.Items[len(out.Items)-1]
			b, _ := json.Marshal(commentCursor{Task: r.TaskId, Time: last.CreatedAt.AsTime(), ID: last.Id})
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			break
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, storage(e)
	}
	return out, nil
}
func (s *Server) CreateComment(ctx context.Context, r *pb.CreateCommentRequest) (*pb.CreateCommentResponse, error) {
	author, e := s.actor(ctx, r.GetActorAssertion(), pb.ContentService_CreateComment_FullMethodName)
	if e != nil {
		return nil, e
	}
	if !taskPattern.MatchString(r.TaskId) || strings.TrimSpace(r.Body) == "" || !utf8.ValidString(r.Body) || strings.ContainsRune(r.Body, 0) || len(r.Body) > 8000 || !keyPattern.MatchString(r.IdempotencyKey) {
		return nil, invalid("invalid_comment")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, storage(e)
	}
	defer tx.Rollback(ctx)
	// Shares the visibility lock with takedown; publication is checked at commit.
	var visible bool
	e = tx.QueryRow(ctx, `SELECT state='published' AND `+catalogPublic+` FROM content.reviewed_tasks t WHERE task_id=$1 FOR SHARE`, r.TaskId).Scan(&visible)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !visible {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if e != nil {
		return nil, storage(e)
	}
	id, e := reviewID("cmt")
	if e != nil {
		return nil, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO content.comments(id,task_id,author_id,body,create_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT(author_id,task_id,create_key) DO NOTHING`, id, r.TaskId, author, r.Body, r.IdempotencyKey)
	if e != nil {
		return nil, storage(e)
	}
	out, e := scanComment(tx.QueryRow(ctx, `SELECT id,task_id,author_id,body,created_at FROM content.comments WHERE task_id=$1 AND author_id=$2 AND create_key=$3`, r.TaskId, author, r.IdempotencyKey))
	if e != nil {
		return nil, storage(e)
	}
	if out.Body != r.Body {
		return nil, status.Error(codes.AlreadyExists, "idempotency_key_conflict")
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, storage(e)
	}
	return &pb.CreateCommentResponse{Comment: out}, nil
}
