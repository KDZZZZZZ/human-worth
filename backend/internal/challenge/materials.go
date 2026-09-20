package challenge

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// ReadMaterial 只转发当前工作项清单中的文件；每块重新检查取消、租约和授权。
func (s *Server) ReadMaterial(q *pb.ReadMaterialRequest, stream grpc.ServerStreamingServer[pb.ReadMaterialResponse]) error {
	if q.Attempt == nil || q.Offset < 0 {
		return invalid("invalid_material_request")
	}
	ctx := stream.Context()
	r, err := s.read(ctx, q.Attempt.RunId)
	if err != nil {
		return err
	}
	if err = s.live(ctx, r, q.Attempt); err != nil {
		return err
	}
	var material *pb.Material
	for _, m := range r.Assignment.Materials {
		if m.Id == q.MaterialId {
			material = m
			break
		}
	}
	if material == nil {
		return denied("material_forbidden")
	}
	if q.Offset > material.Asset.Bytes {
		return invalid("invalid_offset")
	}
	access := materialAccess(r, q.Attempt)
	access.TaskId = material.TaskId
	input, err := s.Asset.ReadAsset(ctx, &asset.ReadAssetRequest{AssetId: material.Asset.Id, Access: access, Offset: q.Offset})
	if err != nil {
		return unavailable()
	}
	offset := q.Offset
	for {
		chunk, err := input.Recv()
		if err == io.EOF {
			if offset != material.Asset.Bytes {
				return conflict("truncated_material")
			}
			return nil
		}
		if err != nil {
			return unavailable()
		}
		if len(chunk.Chunk) == 0 || len(chunk.Chunk) > 65536 || chunk.Offset != offset || chunk.Sha256 != material.Asset.Sha256 || int64(len(chunk.Chunk)) > material.Asset.Bytes-offset {
			return conflict("invalid_material_chunk")
		}
		current, e := s.read(ctx, q.Attempt.RunId)
		if e != nil {
			return e
		}
		if e = s.live(ctx, current, q.Attempt); e != nil {
			return e
		}
		if err = stream.Send(&pb.ReadMaterialResponse{Chunk: chunk.Chunk, Offset: offset, Digest: chunk.Sha256}); err != nil {
			return err
		}
		offset += int64(len(chunk.Chunk))
	}
}

// UploadArtifact 仅接受 E 的本次产物，边传边核对大小和摘要；未完成的临时文件不能成为作品。
func (s *Server) UploadArtifact(stream grpc.ClientStreamingServer[pb.UploadArtifactRequest, pb.UploadArtifactResponse]) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return invalid("upload_metadata_required")
	}
	if first.Attempt == nil || !validID(first.UploadId) || !bounded(first.Filename, 255) || strings.ContainsAny(first.Filename, "/\\") || strings.HasPrefix(first.Filename, ".") || first.Filename == "report.md" || strings.HasSuffix(first.Filename, ".jsonl") || first.Bytes < 0 || first.Bytes > maxMaterialBytes || !validDigest(first.Sha256) || !bounded(first.MediaType, 128) {
		return invalid("invalid_upload")
	}
	r, err := s.read(ctx, first.Attempt.RunId)
	if err != nil {
		return err
	}
	if err = s.live(ctx, r, first.Attempt); err != nil {
		return err
	}
	if r.Assignment.Kind != pb.WorkKind_WORK_KIND_EXECUTE_PACKAGE {
		return denied("upload_forbidden")
	}
	call, err := s.Asset.UploadAsset(ctx)
	if err != nil {
		return unavailable()
	}
	digest := sha256.New()
	var count int64
	current := first
	for {
		if len(current.Chunk) > 65536 || count+int64(len(current.Chunk)) > first.Bytes {
			return invalid("invalid_upload_size")
		}
		if current != first && (current.Attempt != nil || current.UploadId != "" || current.Filename != "" || current.MediaType != "" || current.Bytes != 0 || current.Sha256 != "") {
			return invalid("duplicate_upload_metadata")
		}
		r, err = s.read(ctx, first.Attempt.RunId)
		if err != nil {
			return err
		}
		if err = s.live(ctx, r, first.Attempt); err != nil {
			return err
		}
		frame := &asset.UploadAssetRequest{Chunk: current.Chunk}
		if current == first {
			frame.Access = materialAccess(r, first.Attempt)
			frame.UploadId = first.UploadId
			frame.Filename = first.Filename
			frame.MediaType = first.MediaType
			frame.Bytes = first.Bytes
			frame.Sha256 = first.Sha256
		}
		if err = call.Send(frame); err != nil {
			return unavailable()
		}
		digest.Write(current.Chunk)
		count += int64(len(current.Chunk))
		current, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if count != first.Bytes || hex.EncodeToString(digest.Sum(nil)) != first.Sha256 {
		return invalid("upload_digest_mismatch")
	}
	r, err = s.read(ctx, first.Attempt.RunId)
	if err != nil {
		return err
	}
	if err = s.live(ctx, r, first.Attempt); err != nil {
		return err
	}
	response, err := call.CloseAndRecv()
	if err != nil {
		return unavailable()
	}
	a := response.Asset
	if !validAsset(a) || a.RunId != r.Run.Id || a.AttemptId != first.Attempt.AttemptId || a.Bytes != first.Bytes || a.Sha256 != first.Sha256 || a.Filename != first.Filename || a.MediaType != first.MediaType {
		return conflict("invalid_uploaded_asset")
	}
	check, err := s.Asset.GetAsset(ctx, &asset.GetAssetRequest{AssetId: a.Id, Access: materialAccess(r, first.Attempt)})
	if err != nil || !proto.Equal(check.GetAsset(), a) {
		return conflict("unconfirmed_upload")
	}
	return stream.SendAndClose(&pb.UploadArtifactResponse{Asset: a})
}
