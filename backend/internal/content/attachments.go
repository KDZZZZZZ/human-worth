package content

import (
	"context"
	"errors"
	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

type contentQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func assetDescriptor(ctx context.Context, q contentQuerier, id string) (*assetpb.Asset, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT metadata FROM content.asset_descriptors WHERE asset_id=$1`, id).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "asset_metadata_missing")
	}
	if e != nil {
		return nil, storage(e)
	}
	out := &assetpb.Asset{}
	if protojson.Unmarshal(raw, out) != nil {
		return nil, status.Error(codes.Internal, "invalid_asset_metadata")
	}
	return out, nil
}
func taskAttachments(ctx context.Context, q contentQuerier, task string) ([]*pb.TaskAttachment, error) {
	var raw []byte
	e := q.QueryRow(ctx, `SELECT snapshot FROM content.reviewed_tasks t WHERE task_id=$1 AND state='published' AND `+catalogPublic+``, task).Scan(&raw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "task_not_found")
	}
	if e != nil {
		return nil, storage(e)
	}
	input := &pb.TaskDraftInput{}
	if protojson.Unmarshal(raw, input) != nil {
		return nil, status.Error(codes.Internal, "invalid_review_snapshot")
	}
	out := make([]*pb.TaskAttachment, 0, len(input.TaskAttachments))
	for _, a := range input.TaskAttachments {
		v, e := assetDescriptor(ctx, q, a.AssetId)
		if e != nil {
			return nil, e
		}
		out = append(out, &pb.TaskAttachment{Asset: v, CloudUseAllowed: a.GetCloudUseAllowed()})
	}
	return out, nil
}
