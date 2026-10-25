package voting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func (s *Server) PrepareCatalogChange(ctx context.Context, r *pb.PrepareCatalogChangeRequest) (*pb.PrepareCatalogChangeResponse, error) {
	if err := peer(ctx, "content"); err != nil {
		return nil, err
	}
	if !taskID.MatchString(r.GetTaskId()) || !objectID.MatchString(r.GetChangeId()) || r.ExpectedGeneration < 0 || r.Generation != r.ExpectedGeneration+1 {
		return nil, invalid("invalid_catalog_change")
	}
	raw, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(r)
	hash := sha256.Sum256(raw)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	// Insert missing gate first; races serialize through its primary key.
	_, err = tx.Exec(ctx, `INSERT INTO voting.catalog_gates(task_id,generation,state,change_id) VALUES($1,0,'frozen',$2) ON CONFLICT DO NOTHING`, r.TaskId, r.ChangeId)
	if err != nil {
		return nil, storage(err)
	}
	var generation int64
	var change, state string
	if err = tx.QueryRow(ctx, `SELECT generation,change_id,state FROM voting.catalog_gates WHERE task_id=$1 FOR UPDATE`, r.TaskId).Scan(&generation, &change, &state); err != nil {
		return nil, storage(err)
	}
	var old []byte
	var phase, digest string
	err = tx.QueryRow(ctx, `SELECT request_hash,phase,sha256 FROM voting.catalog_changes WHERE change_id=$1`, r.ChangeId).Scan(&old, &phase, &digest)
	if err == nil {
		if !bytes.Equal(old, hash[:]) {
			return nil, conflict("catalog_change_conflict")
		}
		return &pb.PrepareCatalogChangeResponse{Transition: &pb.CatalogTransition{ChangeId: r.ChangeId, TaskId: r.TaskId, Generation: r.Generation, Phase: phase, Sha256: digest}}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, storage(err)
	}
	if generation != r.ExpectedGeneration || state == "frozen" && generation > 0 {
		return nil, conflict("catalog_generation_conflict")
	}
	_, err = tx.Exec(ctx, `UPDATE voting.catalog_gates SET state='frozen',generation=$2,change_id=$3 WHERE task_id=$1`, r.TaskId, r.Generation, r.ChangeId)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO voting.catalog_changes(change_id,task_id,generation,request_hash,phase) VALUES($1,$2,$3,$4,'prepared')`, r.ChangeId, r.TaskId, r.Generation, hash[:])
	}
	if err != nil {
		return nil, storage(err)
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.PrepareCatalogChangeResponse{Transition: &pb.CatalogTransition{ChangeId: r.ChangeId, TaskId: r.TaskId, Generation: r.Generation, Phase: "prepared"}}, nil
}
func (s *Server) ActivateCatalogSnapshot(ctx context.Context, r *pb.ActivateCatalogSnapshotRequest) (*pb.ActivateCatalogSnapshotResponse, error) {
	if err := peer(ctx, "content"); err != nil {
		return nil, err
	}
	c := r.GetCatalog()
	if err := contract.ValidateCatalog(c); err != nil {
		return nil, status.Error(codes.ResourceExhausted, "catalog_too_large_or_invalid")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	var generation int64
	var change, state, digest string
	var tombstone bool
	err = tx.QueryRow(ctx, `SELECT generation,change_id,state,sha256,tombstone FROM voting.catalog_gates WHERE task_id=$1 FOR UPDATE`, c.TaskId).Scan(&generation, &change, &state, &digest, &tombstone)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, conflict("catalog_not_prepared")
	}
	if err != nil {
		return nil, storage(err)
	}
	// Historical exact activation can be acknowledged, NEVER applied again.
	var oldHash, phase string
	var oldGeneration int64
	var task string
	err = tx.QueryRow(ctx, `SELECT task_id,generation,phase,sha256 FROM voting.catalog_changes WHERE change_id=$1`, r.ChangeId).Scan(&task, &oldGeneration, &phase, &oldHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, conflict("catalog_not_prepared")
	}
	if err != nil {
		return nil, storage(err)
	}
	if task != c.TaskId || oldGeneration != c.Generation {
		return nil, conflict("catalog_change_conflict")
	}
	out := &pb.CatalogTransition{ChangeId: r.ChangeId, TaskId: c.TaskId, Generation: c.Generation, Phase: "activated", Sha256: c.Sha256}
	if phase == "activated" {
		if oldHash != c.Sha256 {
			return nil, conflict("catalog_change_conflict")
		}
		return &pb.ActivateCatalogSnapshotResponse{Transition: out}, nil
	}
	if generation != c.Generation || change != r.ChangeId || state != "frozen" || tombstone && c.State == "open" {
		return nil, conflict("catalog_generation_conflict")
	}
	raw, _ := proto.Marshal(c)
	_, err = tx.Exec(ctx, `UPDATE voting.catalog_gates SET state=$2,sha256=$3,snapshot=$4,activated_at=clock_timestamp(),tombstone=tombstone OR $2='closed' WHERE task_id=$1`, c.TaskId, c.State, c.Sha256, raw)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE voting.catalog_changes SET phase='activated',sha256=$2 WHERE change_id=$1`, r.ChangeId, c.Sha256)
	}
	if err != nil {
		return nil, storage(err)
	}
	if err = updateConclusion(ctx, tx, c); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return &pb.ActivateCatalogSnapshotResponse{Transition: out}, nil
}
func (s *Server) GetCatalogTransition(ctx context.Context, r *pb.GetCatalogTransitionRequest) (*pb.GetCatalogTransitionResponse, error) {
	if err := peer(ctx, "content"); err != nil {
		return nil, err
	}
	out := &pb.CatalogTransition{ChangeId: r.GetChangeId()}
	err := s.DB.QueryRow(ctx, `SELECT task_id,generation,phase,sha256 FROM voting.catalog_changes WHERE change_id=$1`, r.GetChangeId()).Scan(&out.TaskId, &out.Generation, &out.Phase, &out.Sha256)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "catalog_change_not_found")
	}
	if err != nil {
		return nil, storage(err)
	}
	return &pb.GetCatalogTransitionResponse{Transition: out}, nil
}
