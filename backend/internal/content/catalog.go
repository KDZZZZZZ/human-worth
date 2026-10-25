package content

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// One transaction snapshot contains the entire approved set, never a browse page.
func votingCatalog(ctx context.Context, tx pgx.Tx, task string) (*pb.VotingCatalog, bool, error) {
	c := &pb.VotingCatalog{TaskId: task}
	var snapshot []byte
	err := tx.QueryRow(ctx, `SELECT source_revision,catalog_revision,state='published' AND `+catalogPublic+`,snapshot FROM content.reviewed_tasks t WHERE task_id=$1`, task).Scan(&c.TaskRevision, &c.CatalogRevision, &c.Visible, &snapshot)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, status.Error(codes.NotFound, "task_not_found")
	}
	if err != nil {
		return nil, false, storage(err)
	}
	allowed := c.Visible
	ids := []string{}
	assetIDs := []string{}
	if c.Visible {
		rows, e := tx.Query(ctx, `SELECT e.id,COALESCE(e.author_id,d.author_id),e.approved_at,`+publicEntryContent+` FROM content.reviewed_entries e JOIN content.reviewed_tasks t ON t.task_id=e.task_id JOIN content.task_drafts d ON d.id=t.task_id WHERE e.task_id=$1 AND e.state='approved' ORDER BY e.position,e.id`, task)
		if e != nil {
			return nil, false, storage(e)
		}
		defer rows.Close()
		for rows.Next() {
			v := &pb.VotingEntry{TaskId: task, Author: &pb.VotingAuthor{}}
			var raw []byte
			var approved *time.Time
			if e = rows.Scan(&v.Id, &v.Author.Id, &approved, &raw); e != nil {
				return nil, false, storage(e)
			}
			entry := &pb.EntryInput{}
			if protojson.Unmarshal(raw, entry) != nil {
				return nil, false, status.Error(codes.Internal, "invalid_review_snapshot")
			}
			v.Side, v.Title, v.Description, v.Source, v.ApprovedAt = entry.Side, entry.Title, entry.GetDescription(), entry.Source, publicTime(approved)
			allowed = allowed && entry.GetPermissions().GetCloudUse() && workBodyRequired(entry) == nil
			for _, a := range entry.Artifacts {
				if a.Kind == "file" {
					assetIDs = append(assetIDs, a.GetAssetId())
				}
				v.Artifacts = append(v.Artifacts, &pb.VotingArtifact{Kind: a.Kind, AssetId: a.GetAssetId(), Url: a.GetUrl(), Text: a.GetText()})
			}
			c.Entries = append(c.Entries, v)
			ids = append(ids, v.Id)
			if proto.Size(c) > 1<<20 {
				return nil, false, status.Error(codes.ResourceExhausted, "catalog_too_large")
			}
		}
		if e = rows.Err(); e != nil {
			return nil, false, storage(e)
		}
		rows.Close()
		// Legacy approved files can remain public without being eligible for
		// training: a reproducible material snapshot requires verified metadata.
		if len(assetIDs) > 0 {
			var missing bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM unnest($1::text[]) AS a(id) WHERE NOT EXISTS(SELECT 1 FROM content.asset_descriptors d WHERE d.asset_id=a.id))`, assetIDs).Scan(&missing); e != nil {
				return nil, false, storage(e)
			}
			allowed = allowed && !missing
		}
	}
	sort.Strings(ids)
	c.WorkSetHash = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(ids, "\n"))))
	return c, allowed, nil
}

func (s *Server) GetVotingCatalog(ctx context.Context, r *pb.GetVotingCatalogRequest) (*pb.GetVotingCatalogResponse, error) {
	if e := requireContentCaller(ctx, "voting"); e != nil {
		return nil, e
	}
	if !taskPattern.MatchString(r.GetTaskId()) || (r.Purpose != "public" && r.Purpose != "preference") {
		return nil, invalid("invalid_catalog_request")
	}
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, storage(e)
	}
	defer tx.Rollback(ctx)
	c, allowed, e := votingCatalog(ctx, tx, r.TaskId)
	if e != nil {
		return nil, e
	}
	if r.Purpose == "preference" {
		ref := r.GetSnapshot()
		allowed = allowed && keyPattern.MatchString(r.RunId) && ref.GetTaskId() == r.TaskId && ref.TaskRevision == c.TaskRevision && ref.CatalogRevision == c.CatalogRevision && ref.CommentCutoff != nil && ref.CommentCutoff.CheckValid() == nil && !ref.CommentCutoff.AsTime().After(time.Now())
	}
	return &pb.GetVotingCatalogResponse{Catalog: c, PreferenceAllowed: allowed}, nil
}

// New submissions must contain the work itself. Previously approved legacy
// records remain readable; they are never silently rewritten or re-approved.
func workBodyRequired(e *pb.EntryInput) error {
	for _, a := range e.GetArtifacts() {
		if a.GetKind() == "file" || (a.GetKind() == "text" && strings.TrimSpace(a.GetText()) != "") {
			return nil
		}
	}
	return status.Error(codes.FailedPrecondition, "artifact_body_required")
}
