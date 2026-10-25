package voting

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
	"regexp"
	"slices"
	"strings"
	"time"
)

const preferencePolicy = "current-choice-catalog-holdout-v2"

var runID = regexp.MustCompile(`^[A-Za-z0-9_-]{8,160}$`)

func workSet(c *contentpb.VotingCatalogSnapshot) string {
	ids := []string{}
	for _, e := range c.Entries {
		ids = append(ids, e.Id)
	}
	slices.Sort(ids)
	sum := sha256.Sum256([]byte(strings.Join(ids, "\n")))
	return hex.EncodeToString(sum[:])
}

type preferenceQuery interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readPreference(ctx context.Context, q preferenceQuery, r *pb.GetHumanPreferenceSnapshotRequest, hash []byte) (*pb.GetHumanPreferenceSnapshotResponse, error) {
	var old, raw []byte
	err := q.QueryRow(ctx, `SELECT request_hash,payload FROM voting.preference_snapshots WHERE run_id=$1 AND purpose=$2 AND batch_index=$3`, r.RunId, r.Purpose, r.BatchIndex).Scan(&old, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storage(err)
	}
	if !bytes.Equal(old, hash) {
		return nil, conflict("idempotency_conflict")
	}
	out := &pb.GetHumanPreferenceSnapshotResponse{}
	if proto.Unmarshal(raw, out) != nil {
		return nil, status.Error(codes.Internal, "invalid_preference_snapshot")
	}
	return out, nil
}

func (s *Server) GetHumanPreferenceSnapshot(ctx context.Context, r *pb.GetHumanPreferenceSnapshotRequest) (*pb.GetHumanPreferenceSnapshotResponse, error) {
	if err := peer(ctx, "challenge"); err != nil {
		return nil, err
	}
	if !runID.MatchString(r.GetRunId()) || (r.GetPurpose() != "training" && r.GetPurpose() != "validation") || r.GetBatchIndex() < 0 || len(r.GetExcludedTaskIds()) > 4096 {
		return nil, invalid("invalid_preference_request")
	}
	r = proto.Clone(r).(*pb.GetHumanPreferenceSnapshotRequest)
	slices.Sort(r.ExcludedTaskIds)
	r.ExcludedTaskIds = slices.Compact(r.ExcludedTaskIds)
	for _, id := range r.ExcludedTaskIds {
		if !taskID.MatchString(id) {
			return nil, invalid("invalid_task")
		}
	}
	raw, _ := (proto.MarshalOptions{Deterministic: true}).Marshal(r)
	hash := sha256.Sum256(raw)
	out, err := readPreference(ctx, s.DB, r, hash[:])
	if err != nil {
		return nil, err
	}
	if out == nil {
		out, err = s.createPreference(ctx, r, hash[:])
		if err != nil {
			return nil, err
		}
	}
	valid, err := s.CheckHumanPreferenceSnapshot(ctx, &pb.CheckHumanPreferenceSnapshotRequest{SnapshotId: out.SnapshotId, PolicyVersion: out.PolicyVersion})
	if err != nil {
		return nil, err
	}
	if !valid.Valid {
		return nil, status.Error(codes.FailedPrecondition, "preference_snapshot_invalid")
	}
	return out, nil
}

func (s *Server) createPreference(ctx context.Context, r *pb.GetHumanPreferenceSnapshotRequest, hash []byte) (*pb.GetHumanPreferenceSnapshotResponse, error) {
	rows, err := s.DB.Query(ctx, `SELECT task_id FROM voting.catalog_gates g WHERE state='open' AND NOT(task_id=ANY(COALESCE($2::text[],'{}'::text[]))) AND NOT EXISTS(SELECT 1 FROM voting.preference_assignments a WHERE a.run_id=$1 AND a.task_id=g.task_id) AND EXISTS(SELECT 1 FROM voting.current_votes v WHERE v.task_id=g.task_id AND v.generation=g.generation AND v.updated_at>=g.activated_at) ORDER BY task_id LIMIT 256`, r.RunId, r.ExcludedTaskIds)
	if err != nil {
		return nil, storage(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, storage(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, storage(rows.Err())
	}
	out := &pb.GetHumanPreferenceSnapshotResponse{PolicyVersion: preferencePolicy}
	generations := map[string]int64{}
	for _, id := range ids {
		sample, c, err := s.preferenceSample(ctx, id)
		if err != nil {
			return nil, err
		}
		if sample == nil {
			continue
		}
		allowed, err := s.preferenceAllowed(ctx, r.RunId, sample)
		if err != nil {
			return nil, err
		}
		if !allowed {
			continue
		}
		out.Samples = append(out.Samples, sample)
		generations[id] = c.Generation
		if proto.Size(out) > 1<<20 {
			return nil, status.Error(codes.ResourceExhausted, "preference_snapshot_too_large")
		}
		if len(out.Samples) == 20 {
			break
		}
	}
	if len(out.Samples) == 0 || r.Purpose == "training" && len(out.Samples) < 2 {
		// Another replica may have committed this exact request between the
		// initial lookup and the candidate query (which excludes assignments).
		if old, e := readPreference(ctx, s.DB, r, hash); e != nil || old != nil {
			return old, e
		}
		return nil, status.Error(codes.FailedPrecondition, "insufficient_preference_data")
	}
	if r.Purpose == "training" {
		out.Samples = out.Samples[:len(out.Samples)/2]
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return nil, storage(err)
	}
	defer tx.Rollback(ctx)
	// The same run must never receive one task in both training and validation.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,7185429012))`, r.RunId); err != nil {
		return nil, storage(err)
	}
	old, err := readPreference(ctx, tx, r, hash)
	if err != nil {
		return nil, err
	}
	if old != nil {
		return old, nil
	}
	selected := map[string]int64{}
	for _, p := range out.Samples {
		var generation int64
		var state string
		var assigned bool
		if err = tx.QueryRow(ctx, `SELECT generation,state,EXISTS(SELECT 1 FROM voting.preference_assignments WHERE run_id=$2 AND task_id=$1) FROM voting.catalog_gates WHERE task_id=$1 FOR SHARE`, p.Snapshot.TaskId, r.RunId).Scan(&generation, &state, &assigned); err != nil {
			return nil, storage(err)
		}
		if assigned {
			return nil, conflict("preference_assignment_conflict")
		}
		if generation != generations[p.Snapshot.TaskId] || state != "open" {
			return nil, conflict("catalog_changed")
		}
		selected[p.Snapshot.TaskId] = generation
	}
	out.SnapshotId, err = id("pref")
	if err != nil {
		return nil, err
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, storage(err)
	}
	out.ValidUntil = timestamppb.New(now.Add(24 * time.Hour))
	payload, _ := proto.Marshal(out)
	versions, _ := json.Marshal(selected)
	_, err = tx.Exec(ctx, `INSERT INTO voting.preference_snapshots(id,run_id,purpose,batch_index,request_hash,payload,generations) VALUES($1,$2,$3,$4,$5,$6,$7)`, out.SnapshotId, r.RunId, r.Purpose, r.BatchIndex, hash, payload, versions)
	if err != nil {
		return nil, storage(err)
	}
	for _, p := range out.Samples {
		if _, err = tx.Exec(ctx, `INSERT INTO voting.preference_assignments(run_id,task_id,snapshot_id) VALUES($1,$2,$3)`, r.RunId, p.Snapshot.TaskId, out.SnapshotId); err != nil {
			return nil, storage(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, storage(err)
	}
	return out, nil
}

func (s *Server) preferenceSample(ctx context.Context, task string) (*pb.HumanPreference, *contentpb.VotingCatalogSnapshot, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, storage(err)
	}
	defer tx.Rollback(ctx)
	var state string
	var raw []byte
	var generation int64
	var start, end time.Time
	if err = tx.QueryRow(ctx, `SELECT state,snapshot,generation,activated_at,clock_timestamp() FROM voting.catalog_gates WHERE task_id=$1`, task).Scan(&state, &raw, &generation, &start, &end); err != nil {
		return nil, nil, storage(err)
	}
	if state != "open" {
		return nil, nil, nil
	}
	c := &contentpb.VotingCatalogSnapshot{}
	if proto.Unmarshal(raw, c) != nil || contract.ValidateCatalog(c) != nil {
		return nil, nil, status.Error(codes.Internal, "invalid_catalog")
	}
	p := &pb.HumanPreference{Snapshot: &contentpb.SnapshotRef{TaskId: task, TaskRevision: c.TaskRevision, CatalogRevision: c.CatalogRevision, CommentCutoff: timestamppb.New(start.Add(-time.Microsecond))}, WorkSetHash: workSet(c), WindowStart: timestamppb.New(start), WindowEnd: timestamppb.New(end), CountsByWork: map[string]int64{}}
	for _, e := range c.Entries {
		p.CountsByWork[e.Id] = 0
	}
	rows, err := tx.Query(ctx, `SELECT entry_id,count(*) FROM voting.current_votes WHERE task_id=$1 AND generation=$2 AND updated_at>=$3 AND updated_at<$4 GROUP BY entry_id`, task, generation, start, end)
	if err != nil {
		return nil, nil, storage(err)
	}
	for rows.Next() {
		var id string
		var n int64
		if err = rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, nil, storage(err)
		}
		if _, ok := p.CountsByWork[id]; ok {
			p.CountsByWork[id] = n
			p.ValidVoteCount += n
		}
	}
	rows.Close()
	if rows.Err() != nil {
		return nil, nil, storage(rows.Err())
	}
	distinct := map[int64]bool{}
	for _, n := range p.CountsByWork {
		distinct[n] = true
	}
	if p.ValidVoteCount == 0 || len(distinct) < 2 {
		return nil, nil, nil
	}
	return p, c, nil
}
func (s *Server) preferenceAllowed(ctx context.Context, run string, p *pb.HumanPreference) (bool, error) {
	if s.Content == nil {
		return false, status.Error(codes.Unavailable, "content_unavailable")
	}
	response, err := s.Content.GetVotingCatalog(ctx, &contentpb.GetVotingCatalogRequest{TaskId: p.Snapshot.TaskId, Purpose: "preference", Snapshot: p.Snapshot, RunId: run})
	if err != nil {
		return false, err
	}
	c := response.GetCatalog()
	return response.GetPreferenceAllowed() && c.GetVisible() && c.TaskRevision == p.Snapshot.TaskRevision && c.CatalogRevision == p.Snapshot.CatalogRevision && c.WorkSetHash == p.WorkSetHash, nil
}
func (s *Server) CheckHumanPreferenceSnapshot(ctx context.Context, r *pb.CheckHumanPreferenceSnapshotRequest) (*pb.CheckHumanPreferenceSnapshotResponse, error) {
	if err := peer(ctx, "challenge"); err != nil {
		return nil, err
	}
	out := &pb.CheckHumanPreferenceSnapshotResponse{}
	if !objectID.MatchString(r.GetSnapshotId()) || r.GetPolicyVersion() != preferencePolicy {
		return out, nil
	}
	var run string
	var payload, versions []byte
	err := s.DB.QueryRow(ctx, `SELECT run_id,payload,generations FROM voting.preference_snapshots WHERE id=$1`, r.SnapshotId).Scan(&run, &payload, &versions)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return nil, storage(err)
	}
	snapshot := &pb.GetHumanPreferenceSnapshotResponse{}
	var generations map[string]int64
	if proto.Unmarshal(payload, snapshot) != nil || json.Unmarshal(versions, &generations) != nil {
		return nil, status.Error(codes.Internal, "invalid_preference_snapshot")
	}
	if snapshot.PolicyVersion != r.PolicyVersion || snapshot.ValidUntil == nil || !snapshot.ValidUntil.AsTime().After(time.Now()) {
		return out, nil
	}
	for _, p := range snapshot.Samples {
		var generation int64
		var state string
		if err = s.DB.QueryRow(ctx, `SELECT generation,state FROM voting.catalog_gates WHERE task_id=$1`, p.Snapshot.TaskId).Scan(&generation, &state); err != nil {
			return nil, storage(err)
		}
		if generation != generations[p.Snapshot.TaskId] || state != "open" {
			return out, nil
		}
		allowed, err := s.preferenceAllowed(ctx, run, p)
		if err != nil {
			return nil, err
		}
		if !allowed {
			return out, nil
		}
	}
	out.Valid = true
	return out, nil
}
