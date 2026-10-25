package content

import (
	"context"
	"errors"
	"fmt"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"time"
)

func challengeSnapshot(ctx context.Context, tx pgx.Tx, ref *pb.SnapshotRef) (int64, int64, error) {
	if ref == nil || !taskPattern.MatchString(ref.TaskId) || ref.TaskRevision < 0 || ref.CatalogRevision < 0 || (ref.CommentCutoff != nil && (ref.CommentCutoff.CheckValid() != nil || ref.CommentCutoff.AsTime().After(time.Now().Add(time.Second)))) {
		return 0, 0, invalid("invalid_snapshot")
	}
	var task, catalog int64
	var visible bool
	e := tx.QueryRow(ctx, `SELECT source_revision,catalog_revision,state='published' AND `+catalogPublic+` FROM content.reviewed_tasks t WHERE task_id=$1`, ref.TaskId).Scan(&task, &catalog, &visible)
	if errors.Is(e, pgx.ErrNoRows) || e == nil && !visible {
		return 0, 0, status.Error(codes.NotFound, "task_not_found")
	}
	if e != nil {
		return 0, 0, storage(e)
	}
	if ref.TaskRevision != 0 && ref.TaskRevision != task || ref.CatalogRevision != 0 && ref.CatalogRevision != catalog {
		return 0, 0, status.Error(codes.FailedPrecondition, "snapshot_changed")
	}
	return task, catalog, nil
}
func (s *Server) challengeRead(ctx context.Context, ref *pb.SnapshotRef) (pgx.Tx, int64, int64, error) {
	if e := requireContentCaller(ctx, "challenge"); e != nil {
		return nil, 0, 0, e
	}
	tx, e := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, 0, 0, storage(e)
	}
	task, catalog, e := challengeSnapshot(ctx, tx, ref)
	if e != nil {
		tx.Rollback(ctx)
		return nil, 0, 0, e
	}
	return tx, task, catalog, nil
}
func (s *Server) GetChallengeTaskDescription(ctx context.Context, r *pb.GetChallengeTaskDescriptionRequest) (*pb.GetChallengeTaskDescriptionResponse, error) {
	tx, revision, _, e := s.challengeRead(ctx, r.GetSnapshot())
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	task := &pb.TaskDescription{TaskId: r.Snapshot.TaskId, Revision: revision}
	e = tx.QueryRow(ctx, `SELECT snapshot->>'description' FROM content.reviewed_tasks WHERE task_id=$1`, task.TaskId).Scan(&task.Description)
	if e != nil {
		return nil, storage(e)
	}
	attachments, e := taskAttachments(ctx, tx, task.TaskId)
	if e != nil {
		return nil, e
	}
	for _, a := range attachments {
		if a.CloudUseAllowed {
			task.Attachments = append(task.Attachments, a)
		}
	}
	return &pb.GetChallengeTaskDescriptionResponse{Task: task, Visible: true}, nil
}
func (s *Server) ListChallengeWorks(ctx context.Context, r *pb.ListChallengeWorksRequest) (*pb.ListChallengeWorksResponse, error) {
	tx, _, revision, e := s.challengeRead(ctx, r.GetSnapshot())
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	catalog, allowed, e := votingCatalog(ctx, tx, r.Snapshot.TaskId)
	if e != nil {
		return nil, e
	}
	if !allowed {
		return nil, status.Error(codes.PermissionDenied, "cloud_use_forbidden")
	}
	if len(catalog.Entries) > 100 {
		return nil, status.Error(codes.ResourceExhausted, "materials_too_large")
	}
	out := &pb.ListChallengeWorksResponse{CatalogRevision: revision}
	for _, entry := range catalog.Entries {
		work := &pb.Work{Id: entry.Id}
		for _, a := range entry.Artifacts {
			v := &pb.Artifact{}
			switch a.Kind {
			case "text":
				v.Value = &pb.Artifact_Text{Text: a.Text}
			case "link":
				v.Value = &pb.Artifact_Link{Link: a.Url}
			case "file":
				asset, e := assetDescriptor(ctx, tx, a.AssetId)
				if e != nil {
					return nil, e
				}
				v.Value = &pb.Artifact_File{File: asset}
			default:
				return nil, status.Error(codes.Internal, "invalid_artifact")
			}
			work.Artifacts = append(work.Artifacts, v)
		}
		out.Works = append(out.Works, work)
	}
	return out, nil
}
func (s *Server) ListChallengeComments(ctx context.Context, r *pb.ListChallengeCommentsRequest) (*pb.ListChallengeCommentsResponse, error) {
	tx, _, _, e := s.challengeRead(ctx, r.GetSnapshot())
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	cutoff := time.Now().UTC()
	if r.Snapshot.CommentCutoff != nil {
		cutoff = r.Snapshot.CommentCutoff.AsTime()
	}
	rows, e := tx.Query(ctx, `SELECT id,body FROM content.comments WHERE task_id=$1 AND state='visible' AND created_at<=$2 ORDER BY created_at,id LIMIT 501`, r.Snapshot.TaskId, cutoff)
	if e != nil {
		return nil, storage(e)
	}
	defer rows.Close()
	out := &pb.ListChallengeCommentsResponse{CutoffAt: timestamppb.New(cutoff)}
	for rows.Next() {
		c := &pb.Comment{}
		if e = rows.Scan(&c.Id, &c.Body); e != nil {
			return nil, storage(e)
		}
		out.Comments = append(out.Comments, c)
	}
	if e = rows.Err(); e != nil {
		return nil, storage(e)
	}
	if len(out.Comments) > 500 {
		return nil, status.Error(codes.ResourceExhausted, "materials_too_large")
	}
	return out, nil
}
func (s *Server) CheckChallengeMaterials(ctx context.Context, r *pb.CheckChallengeMaterialsRequest) (*pb.CheckChallengeMaterialsResponse, error) {
	if !keyPattern.MatchString(r.GetRunId()) || len(r.WorkIds) > 100 || len(r.Materials) > 3200 {
		return nil, invalid("invalid_material_request")
	}
	tx, taskRev, catalogRev, e := s.challengeRead(ctx, r.GetSnapshot())
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	out := &pb.CheckChallengeMaterialsResponse{AuthorizationVersion: fmt.Sprintf("%d:%d", taskRev, catalogRev)}
	rank := r.Purpose == "WORK_KIND_TRAIN_RANKER" || r.Purpose == "WORK_KIND_VALIDATE_RANKER" || r.Purpose == "WORK_KIND_RANK_WORKS" || r.Purpose == "WORK_KIND_REFINE_RANKER"
	if !rank && r.Purpose != "WORK_KIND_PACK_TASK" && r.Purpose != "WORK_KIND_EXECUTE_PACKAGE" && r.Purpose != "WORK_KIND_EXPLAIN_OWN_WORK" {
		return nil, invalid("invalid_material_purpose")
	}
	if !rank && len(r.WorkIds) > 0 {
		return out, nil
	}
	for _, id := range r.WorkIds {
		var allowed bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content.reviewed_entries e JOIN content.reviewed_tasks t ON t.task_id=e.task_id WHERE e.task_id=$1 AND e.id=$2 AND e.state='approved' AND (`+publicEntryContent+`->'permissions'->>'cloudUse')='true')`, r.Snapshot.TaskId, id).Scan(&allowed)
		if e != nil {
			return nil, storage(e)
		}
		if !allowed {
			return out, nil
		}
	}
	for _, m := range r.Materials {
		if m == nil {
			return nil, invalid("invalid_material")
		}
		var allowed bool
		switch m.SourceKind {
		case "task":
			if m.SourceId != r.Snapshot.TaskId {
				return out, nil
			}
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content.reviewed_tasks WHERE task_id=$1 AND COALESCE(snapshot->'taskAttachments','[]'::jsonb) @> jsonb_build_array(jsonb_build_object('assetId',$2::text,'cloudUseAllowed',true)))`, r.Snapshot.TaskId, m.AssetId).Scan(&allowed)
		case "work":
			if !rank {
				return out, nil
			}
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM content.reviewed_entries e JOIN content.reviewed_tasks t ON t.task_id=e.task_id WHERE e.task_id=$1 AND e.id=$2 AND e.state='approved' AND (`+publicEntryContent+`->'permissions'->>'cloudUse')='true' AND (`+publicEntryContent+`->'artifacts') @> jsonb_build_array(jsonb_build_object('kind','file','assetId',$3::text)))`, r.Snapshot.TaskId, m.SourceId, m.AssetId).Scan(&allowed)
		default:
			return out, nil
		}
		if e != nil {
			return nil, storage(e)
		}
		if !allowed {
			return out, nil
		}
	}
	out.Allowed = true
	return out, nil
}
