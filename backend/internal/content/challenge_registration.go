package content

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"time"
)

func (s *Server) OpenRunRegistration(ctx context.Context, r *pb.OpenRunRegistrationRequest) (*pb.OpenRunRegistrationResponse, error) {
	if e := requireContentCaller(ctx, "challenge"); e != nil {
		return nil, e
	}
	if !keyPattern.MatchString(r.RunId) || !taskPattern.MatchString(r.TaskId) || r.AdministratorId == "" || len(r.AdministratorId) > 128 || strings.TrimSpace(r.ExecutorModel) == "" || len(r.ExecutorModel) > 256 {
		return nil, invalid("invalid_registration")
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, storage(e)
	}
	defer tx.Rollback(ctx)
	_, e = tx.Exec(ctx, `INSERT INTO content.run_registrations(run_id,task_id,state,administrator_id,executor_model) VALUES($1,$2,'open',$3,$4) ON CONFLICT(run_id) DO NOTHING`, r.RunId, r.TaskId, r.AdministratorId, r.ExecutorModel)
	if e != nil {
		return nil, storage(e)
	}
	var task, state string
	var admin, model *string
	e = tx.QueryRow(ctx, `SELECT task_id,state,administrator_id,executor_model FROM content.run_registrations WHERE run_id=$1 FOR UPDATE`, r.RunId).Scan(&task, &state, &admin, &model)
	if e != nil {
		return nil, storage(e)
	}
	if task != r.TaskId {
		return nil, status.Error(codes.AlreadyExists, "registration_conflict")
	}
	if state == "open" {
		if admin == nil || model == nil || *admin != r.AdministratorId || *model != r.ExecutorModel {
			return nil, status.Error(codes.AlreadyExists, "registration_conflict")
		}
		var visible bool
		e = tx.QueryRow(ctx, `SELECT state='published' FROM content.reviewed_tasks WHERE task_id=$1 FOR SHARE`, task).Scan(&visible)
		if errors.Is(e, pgx.ErrNoRows) || e == nil && !visible {
			return nil, status.Error(codes.FailedPrecondition, "task_unavailable")
		}
		if e != nil {
			return nil, storage(e)
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, storage(e)
	}
	return &pb.OpenRunRegistrationResponse{State: state}, nil
}
func (s *Server) CloseRunRegistration(ctx context.Context, r *pb.CloseRunRegistrationRequest) (*pb.CloseRunRegistrationResponse, error) {
	if e := requireContentCaller(ctx, "challenge"); e != nil {
		return nil, e
	}
	if !keyPattern.MatchString(r.RunId) || !taskPattern.MatchString(r.TaskId) {
		return nil, invalid("invalid_registration")
	}
	var task string
	e := s.DB.QueryRow(ctx, `INSERT INTO content.run_registrations(run_id,task_id,state) VALUES($1,$2,'closed') ON CONFLICT(run_id) DO UPDATE SET state='closed' WHERE content.run_registrations.task_id=EXCLUDED.task_id RETURNING task_id`, r.RunId, r.TaskId).Scan(&task)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.AlreadyExists, "registration_conflict")
	}
	if e != nil {
		return nil, storage(e)
	}
	return &pb.CloseRunRegistrationResponse{State: "closed"}, nil
}
func registrationReceipt(ctx context.Context, q contentQuerier, run, candidate string) (*pb.RegistrationReceipt, []byte, error) {
	out := &pb.RegistrationReceipt{RunId: run, CandidateId: candidate}
	var hash []byte
	e := q.QueryRow(ctx, `SELECT r.request_hash,s.id,s.review_id,s.state FROM content.challenge_registrations r JOIN content.entry_submissions s ON s.id=r.entry_id WHERE r.run_id=$1 AND r.candidate_id=$2`, run, candidate).Scan(&hash, &out.EntryId, &out.ReviewId, &out.ReviewState)
	return out, hash, e
}
func (s *Server) GetChallengeRegistration(ctx context.Context, r *pb.GetChallengeRegistrationRequest) (*pb.GetChallengeRegistrationResponse, error) {
	if e := requireContentCaller(ctx, "challenge"); e != nil {
		return nil, e
	}
	out, _, e := registrationReceipt(ctx, s.DB, r.RunId, r.CandidateId)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "registration_not_found")
	}
	if e != nil {
		return nil, storage(e)
	}
	return &pb.GetChallengeRegistrationResponse{Receipt: out}, nil
}
func (s *Server) RegisterChallengeCandidate(ctx context.Context, r *pb.RegisterChallengeCandidateRequest) (*pb.RegisterChallengeCandidateResponse, error) {
	if e := requireContentCaller(ctx, "challenge"); e != nil {
		return nil, e
	}
	digest, e := hex.DecodeString(r.GetExecutorConfigHash())
	if !keyPattern.MatchString(r.RunId) || !keyPattern.MatchString(r.CandidateId) || !keyPattern.MatchString(r.SourceAttemptId) || !taskPattern.MatchString(r.TaskId) || r.Work == nil || r.Work.Id != r.CandidateId || e != nil || len(digest) != 32 || len(r.Work.Artifacts) == 0 || len(r.Work.Artifacts) > 32 {
		return nil, invalid("invalid_candidate")
	}
	wire, e := (proto.MarshalOptions{Deterministic: true}).Marshal(r)
	if e != nil || len(wire) > 1<<20 {
		return nil, invalid("invalid_candidate")
	}
	hash := sha256.Sum256(wire)
	// A receipt survives closure, cancellation and review. Find it before any
	// external call so a lost successful response never creates a second work.
	if old, h, e := registrationReceipt(ctx, s.DB, r.RunId, r.CandidateId); e == nil {
		if !bytes.Equal(h, hash[:]) {
			return nil, status.Error(codes.AlreadyExists, "registration_conflict")
		}
		return &pb.RegisterChallengeCandidateResponse{Receipt: old}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, storage(e)
	}
	entry := &pb.EntryInput{Side: "agent", Title: "Cloud challenge result", Description: proto.String("Generated by run " + r.RunId), Source: "Human Worth cloud challenge", Permissions: &pb.PermissionsInput{CloudUse: proto.Bool(true)}}
	assets := []*assetpb.Asset{}
	for _, a := range r.Work.Artifacts {
		switch v := a.GetValue().(type) {
		case *pb.Artifact_Text:
			entry.Artifacts = append(entry.Artifacts, &pb.ArtifactInput{Kind: "text", Text: &v.Text})
		case *pb.Artifact_Link:
			entry.Artifacts = append(entry.Artifacts, &pb.ArtifactInput{Kind: "link", Url: &v.Link})
		case *pb.Artifact_File:
			if v.File == nil || v.File.RunId != r.RunId || v.File.AttemptId != r.SourceAttemptId || s.Asset == nil {
				return nil, status.Error(codes.FailedPrecondition, "candidate_asset_unavailable")
			}
			checked, e := s.Asset.GetAsset(ctx, &assetpb.GetAssetRequest{AssetId: v.File.Id, Access: &assetpb.Access{TaskId: r.TaskId, RunId: r.RunId, AttemptId: r.SourceAttemptId, Purpose: "output"}})
			if e != nil {
				return nil, e
			}
			if !proto.Equal(checked.Asset, v.File) {
				return nil, status.Error(codes.FailedPrecondition, "candidate_asset_unavailable")
			}
			assets = append(assets, v.File)
			entry.Artifacts = append(entry.Artifacts, &pb.ArtifactInput{Kind: "file", AssetId: &v.File.Id})
		default:
			return nil, invalid("invalid_candidate")
		}
	}
	if e = workBodyRequired(entry); e != nil {
		return nil, e
	}
	tx, e := s.DB.Begin(ctx)
	if e != nil {
		return nil, storage(e)
	}
	defer tx.Rollback(ctx)
	var task, state, admin, model string
	e = tx.QueryRow(ctx, `SELECT task_id,state,COALESCE(administrator_id,''),COALESCE(executor_model,'') FROM content.run_registrations WHERE run_id=$1 FOR UPDATE`, r.RunId).Scan(&task, &state, &admin, &model)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, status.Error(codes.FailedPrecondition, "registration_closed")
	}
	if e != nil {
		return nil, storage(e)
	}
	if old, h, e := registrationReceipt(ctx, tx, r.RunId, r.CandidateId); e == nil {
		if !bytes.Equal(h, hash[:]) {
			return nil, status.Error(codes.AlreadyExists, "registration_conflict")
		}
		return &pb.RegisterChallengeCandidateResponse{Receipt: old}, nil
	} else if !errors.Is(e, pgx.ErrNoRows) {
		return nil, storage(e)
	}
	if state != "open" || task != r.TaskId || admin == "" || model == "" {
		return nil, status.Error(codes.FailedPrecondition, "registration_closed")
	}
	if e = lockEntryParent(ctx, tx, task); e != nil {
		return nil, e
	}
	config, _ := structpb.NewStruct(map[string]any{"executorConfigHash": r.ExecutorConfigHash})
	entry.AgentConfiguration = &pb.AgentConfigurationInput{Model: model, Parameters: config}
	data, e := validateEntry(entry)
	if e != nil {
		return nil, e
	}
	contentHash := sha256.Sum256(data)
	id, e := reviewID("ent")
	if e != nil {
		return nil, e
	}
	review, e := reviewID("rev")
	if e != nil {
		return nil, e
	}
	var now time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
		return nil, storage(e)
	}
	key := fmtCandidateKey(r.RunId, r.CandidateId)
	_, e = tx.Exec(ctx, `INSERT INTO content.entry_submissions(id,task_id,author_id,origin,create_key,create_hash,revision,state,content,review_id,created_at,updated_at) VALUES($1,$2,$3,'cloud',$4,$5,1,'pending_review',$6,$7,$8,$8)`, id, task, admin, key, contentHash[:], data, review, now)
	if e != nil {
		return nil, storage(e)
	}
	sub := &pb.EntrySubmission{Id: id, TaskId: task, AuthorId: admin, Origin: "cloud", Revision: 1, State: "pending_review", Content: entry, ReviewId: review, CreatedAt: timestamppb.New(now), UpdatedAt: timestamppb.New(now)}
	if e = persistEntryReview(ctx, tx, sub, contentHash[:]); e != nil {
		return nil, e
	}
	for _, asset := range assets {
		raw, e := protojson.Marshal(asset)
		if e != nil {
			return nil, storage(e)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO content.asset_descriptors(asset_id,metadata) VALUES($1,$2) ON CONFLICT DO NOTHING`, asset.Id, raw); e != nil {
			return nil, storage(e)
		}
	}
	_, e = tx.Exec(ctx, `INSERT INTO content.challenge_registrations(run_id,candidate_id,request_hash,entry_id) VALUES($1,$2,$3,$4)`, r.RunId, r.CandidateId, hash[:], id)
	if e != nil {
		return nil, storage(e)
	}
	if e = tx.Commit(ctx); e != nil {
		return nil, storage(e)
	}
	return &pb.RegisterChallengeCandidateResponse{Receipt: &pb.RegistrationReceipt{RunId: r.RunId, CandidateId: r.CandidateId, EntryId: id, ReviewId: review, ReviewState: "pending_review", Created: true}}, nil
}
func fmtCandidateKey(run, candidate string) string {
	h := sha256.Sum256([]byte(run + "\x00" + candidate))
	return "cloud_" + hex.EncodeToString(h[:])
}
