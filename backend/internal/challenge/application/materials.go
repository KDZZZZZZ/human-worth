package application

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strings"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

// ReadMaterial 只转发当前工作项清单中的文件；每块重新检查取消、租约和授权。
func (s *Service) ReadMaterial(q *dto.ReadMaterialRequest, stream MaterialStream) error {
	if q.Attempt == nil || q.Offset < 0 {
		return domain.Invalid("invalid_material_request")
	}
	ctx := stream.Context()
	r, err := s.read(ctx, q.Attempt.RunId)
	if err != nil {
		return err
	}
	if err = s.live(ctx, r, q.Attempt); err != nil {
		return err
	}
	var material *domain.Material
	for _, m := range r.Assignment.Materials {
		if m.Id == q.MaterialId {
			material = m
			break
		}
	}
	if material == nil {
		return domain.Denied("material_forbidden")
	}
	if q.Offset > material.Asset.Bytes {
		return domain.Invalid("invalid_offset")
	}
	access := materialAccess(r, q.Attempt)
	access.TaskId = material.TaskId
	input, err := s.asset.ReadAsset(ctx, &dto.ReadAssetRequest{AssetId: material.Asset.Id, Access: access, Offset: q.Offset})
	if err != nil {
		return domain.UnavailableError()
	}
	offset := q.Offset
	for {
		chunk, err := input.Recv()
		if err == io.EOF {
			if offset != material.Asset.Bytes {
				return domain.Conflict("truncated_material")
			}
			return nil
		}
		if err != nil {
			return domain.UnavailableError()
		}
		if len(chunk.Chunk) == 0 || len(chunk.Chunk) > 65536 || chunk.Offset != offset || chunk.Sha256 != material.Asset.Sha256 || int64(len(chunk.Chunk)) > material.Asset.Bytes-offset {
			return domain.Conflict("invalid_material_chunk")
		}
		current, e := s.read(ctx, q.Attempt.RunId)
		if e != nil {
			return e
		}
		if e = s.live(ctx, current, q.Attempt); e != nil {
			return e
		}
		if err = stream.Send(&dto.ReadMaterialResponse{Chunk: chunk.Chunk, Offset: offset, Digest: chunk.Sha256}); err != nil {
			return err
		}
		offset += int64(len(chunk.Chunk))
	}
}

// UploadArtifact 仅接受 E 的本次产物，边传边核对大小和摘要；未完成的临时文件不能成为作品。
func (s *Service) UploadArtifact(stream UploadStream) error {
	ctx := stream.Context()
	first, err := stream.Recv()
	if err != nil {
		return domain.Invalid("upload_metadata_required")
	}
	if first.Attempt == nil || !domain.ValidID(first.UploadId) || !domain.Bounded(first.Filename, 255) || strings.ContainsAny(first.Filename, "/\\") || strings.HasPrefix(first.Filename, ".") || first.Filename == "report.md" || strings.HasSuffix(first.Filename, ".jsonl") || first.Bytes < 0 || first.Bytes > domain.MaxMaterialBytes || !domain.ValidDigest(first.Sha256) || !domain.Bounded(first.MediaType, 128) {
		return domain.Invalid("invalid_upload")
	}
	r, err := s.read(ctx, first.Attempt.RunId)
	if err != nil {
		return err
	}
	if err = s.live(ctx, r, first.Attempt); err != nil {
		return err
	}
	if r.Assignment.Kind != domain.WorkKind_WORK_KIND_EXECUTE_PACKAGE {
		return domain.Denied("upload_forbidden")
	}
	call, err := s.asset.UploadAsset(ctx)
	if err != nil {
		return domain.UnavailableError()
	}
	digest := sha256.New()
	var count int64
	current := first
	for {
		if len(current.Chunk) > 65536 || count+int64(len(current.Chunk)) > first.Bytes {
			return domain.Invalid("invalid_upload_size")
		}
		if current != first && (current.Attempt != nil || current.UploadId != "" || current.Filename != "" || current.MediaType != "" || current.Bytes != 0 || current.Sha256 != "") {
			return domain.Invalid("duplicate_upload_metadata")
		}
		r, err = s.read(ctx, first.Attempt.RunId)
		if err != nil {
			return err
		}
		if err = s.live(ctx, r, first.Attempt); err != nil {
			return err
		}
		frame := &dto.UploadAssetRequest{Chunk: current.Chunk}
		if current == first {
			frame.Access = materialAccess(r, first.Attempt)
			frame.UploadId = first.UploadId
			frame.Filename = first.Filename
			frame.MediaType = first.MediaType
			frame.Bytes = first.Bytes
			frame.Sha256 = first.Sha256
		}
		if err = call.Send(frame); err != nil {
			return domain.UnavailableError()
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
		return domain.Invalid("upload_digest_mismatch")
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
		return domain.UnavailableError()
	}
	a := response.Asset
	if !domain.ValidAsset(a) || a.RunId != r.Run.Id || a.AttemptId != first.Attempt.AttemptId || a.Bytes != first.Bytes || a.Sha256 != first.Sha256 || a.Filename != first.Filename || a.MediaType != first.MediaType {
		return domain.Conflict("invalid_uploaded_asset")
	}
	check, err := s.asset.GetAsset(ctx, &dto.GetAssetRequest{AssetId: a.Id, Access: materialAccess(r, first.Attempt)})
	if err != nil || check.GetAsset() == nil || *check.GetAsset() != *a {
		return domain.Conflict("unconfirmed_upload")
	}
	return stream.SendAndClose(&dto.UploadArtifactResponse{Asset: a})
}
