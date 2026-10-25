package content

import (
	"context"
	"encoding/base64"
	"encoding/json"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"google.golang.org/protobuf/proto"
	"strings"
	"time"
	"unicode/utf8"
)

type discoveryCursor struct {
	Order     string    `json:"order"`
	Category  *string   `json:"category"`
	Query     string    `json:"query"`
	Cutoff    time.Time `json:"cutoff"`
	Heat      float64   `json:"heat"`
	Published time.Time `json:"published"`
	ID        string    `json:"id"`
}

func (s *Server) ListDiscoverableTasks(ctx context.Context, r *pb.ListDiscoverableTasksRequest) (*pb.ListDiscoverableTasksResponse, error) {
	if e := requireContentCaller(ctx, "discovery"); e != nil {
		return nil, e
	}
	order := r.Order
	if order == "" {
		order = "recommended"
	}
	limit := r.Limit
	if limit == 0 {
		limit = 20
	}
	if (order != "recommended" && order != "hot") || limit < 1 || limit > 100 || (r.Category != nil && !validCategory(*r.Category)) {
		return nil, invalid("invalid_request")
	}
	if r.Query != nil && (strings.TrimSpace(*r.Query) == "" || !utf8.ValidString(*r.Query) || utf8.RuneCountInString(*r.Query) > 200 || strings.ContainsRune(*r.Query, 0)) {
		return nil, invalid("invalid_request")
	}
	c := discoveryCursor{Order: order, Category: r.Category, Cutoff: time.Now().UTC(), Heat: 1e20, Published: time.Now().UTC().Add(time.Second), ID: "~"}
	c.Query = r.GetQuery()
	if r.Cursor != "" {
		b, e := base64.RawURLEncoding.DecodeString(r.Cursor)
		if e != nil || len(b) > 2048 || json.Unmarshal(b, &c) != nil || c.Order != order || c.Cutoff.IsZero() || c.ID == "" || ((c.Category == nil) != (r.Category == nil)) || (c.Category != nil && *c.Category != *r.Category) {
			return nil, invalid("invalid_cursor")
		}
		if c.Query != r.GetQuery() {
			return nil, invalid("invalid_cursor")
		}
	}
	// Heat uses recent distinct discussion participants and approved additions;
	// it deliberately has no vote count/rank/share input. A cursor freezes time.
	rows, e := s.DB.Query(ctx, `WITH cards AS (
 SELECT t.task_id,t.source_revision,t.snapshot->>'title' title,t.snapshot->>'summary' summary,t.snapshot->>'description' description,t.snapshot->>'category' category,d.author_id,t.published_at,
 (SELECT count(DISTINCT c.author_id) FROM content.comments c WHERE c.task_id=t.task_id AND c.state='visible' AND c.created_at BETWEEN $1::timestamptz-interval '7 days' AND $1) +
 (SELECT count(*) FROM content.reviewed_entries e WHERE e.task_id=t.task_id AND e.state='approved' AND e.approved_at BETWEEN $1::timestamptz-interval '7 days' AND $1) heat
 FROM content.reviewed_tasks t JOIN content.task_drafts d ON d.id=t.task_id WHERE t.state='published' AND `+catalogPublic+` AND (t.published_at IS NULL OR t.published_at<=$1) AND ($2::text IS NULL OR t.snapshot->>'category'=$2)
 AND ($8='' OR strpos(lower(concat_ws(' ',t.snapshot->>'title',t.snapshot->>'summary',t.snapshot->>'description')),lower($8))>0)
 ) SELECT task_id,source_revision,title,summary,description,category,author_id,published_at,heat FROM cards
 WHERE (CASE WHEN $3='hot' THEN heat::float8 ELSE 0 END,COALESCE(published_at,'epoch'::timestamptz),task_id)<($4,$5,$6)
 ORDER BY CASE WHEN $3='hot' THEN heat ELSE 0 END DESC,COALESCE(published_at,'epoch'::timestamptz) DESC,task_id DESC LIMIT $7`, c.Cutoff, r.Category, order, c.Heat, c.Published, c.ID, limit+1, r.GetQuery())
	if e != nil {
		return nil, storage(e)
	}
	defer rows.Close()
	out := &pb.ListDiscoverableTasksResponse{}
	for rows.Next() {
		t := &pb.PublicTask{Author: &pb.PublicAuthor{}}
		var published *time.Time
		var heat float64
		if e = rows.Scan(&t.Id, &t.Revision, &t.Title, &t.Summary, &t.Description, &t.Category, &t.Author.Id, &published, &heat); e != nil {
			return nil, storage(e)
		}
		if len(out.Items) == int(limit) {
			b, _ := json.Marshal(c)
			out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
			break
		}
		t.PublishedAt = publicTime(published)
		t.Heat = proto.Float64(heat)
		out.Items = append(out.Items, t)
		c.Heat = 0
		if order == "hot" {
			c.Heat = heat
		}
		c.Published = time.Unix(0, 0).UTC()
		if published != nil {
			c.Published = *published
		}
		c.ID = t.Id
	}
	if e = rows.Err(); e != nil {
		return nil, storage(e)
	}
	rows.Close()
	for _, item := range out.Items {
		attachments, e := taskAttachments(ctx, s.DB, item.Id)
		if e != nil {
			return nil, e
		}
		for _, attachment := range attachments {
			item.TaskAttachments = append(item.TaskAttachments, attachment.Asset)
		}
	}
	return out, nil
}
