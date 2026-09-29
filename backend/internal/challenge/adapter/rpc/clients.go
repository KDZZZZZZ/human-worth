package rpc

import (
	"context"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	challenge "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identity "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/grpc"
)

type Clients struct {
	Identity identity.IdentityServiceClient
	Content  content.ContentServiceClient
	Asset    asset.AssetServiceClient
	Voting   voting.VotingServiceClient
}

var adminMethods = map[string]string{

	"StartRun":          challenge.ChallengeService_StartRun_FullMethodName,
	"GetRun":            challenge.ChallengeService_GetRun_FullMethodName,
	"ListRuns":          challenge.ChallengeService_ListRuns_FullMethodName,
	"GetRunSummary":     challenge.ChallengeService_GetRunSummary_FullMethodName,
	"CancelRun":         challenge.ChallengeService_CancelRun_FullMethodName,
	"RestartRun":        challenge.ChallengeService_RestartRun_FullMethodName,
	"RegisterCandidate": challenge.ChallengeService_RegisterCandidate_FullMethodName,
}

func (c Clients) Verify(ctx context.Context, assertion, operation string) (string, error) {
	if assertion == "" {
		return "", domain.Reject(domain.Unauthenticated, "administrator_required")
	}
	method, ok := adminMethods[operation]
	if !ok {
		return "", domain.Denied("administrator_required")
	}
	v, err := c.Identity.VerifyActor(ctx, &identity.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if err != nil {
		return "", protobuf.FromError(err)
	}
	if v.Principal == nil || v.Principal.Role != identity.Role_ROLE_ADMIN || v.Principal.ClientKind != identity.ClientKind_CLIENT_KIND_WEB_SESSION || v.Principal.AccountId == "" {
		return "", domain.Denied("administrator_required")
	}
	return v.Principal.AccountId, nil
}

func (c Clients) GetChallengeTaskDescription(ctx context.Context, q *dto.GetChallengeTaskDescriptionRequest) (*dto.GetChallengeTaskDescriptionResponse, error) {
	v, err := c.Content.GetChallengeTaskDescription(ctx, protobuf.ToGetChallengeTaskDescriptionRequest(q))
	return protobuf.FromGetChallengeTaskDescriptionResponse(v), protobuf.FromError(err)
}
func (c Clients) ListChallengeWorks(ctx context.Context, q *dto.ListChallengeWorksRequest) (*dto.ListChallengeWorksResponse, error) {
	v, err := c.Content.ListChallengeWorks(ctx, protobuf.ToListChallengeWorksRequest(q))
	return protobuf.FromListChallengeWorksResponse(v), protobuf.FromError(err)
}
func (c Clients) ListChallengeComments(ctx context.Context, q *dto.ListChallengeCommentsRequest) (*dto.ListChallengeCommentsResponse, error) {
	v, err := c.Content.ListChallengeComments(ctx, protobuf.ToListChallengeCommentsRequest(q))
	return protobuf.FromListChallengeCommentsResponse(v), protobuf.FromError(err)
}
func (c Clients) CheckChallengeMaterials(ctx context.Context, q *dto.CheckChallengeMaterialsRequest) (*dto.CheckChallengeMaterialsResponse, error) {
	v, err := c.Content.CheckChallengeMaterials(ctx, protobuf.ToCheckChallengeMaterialsRequest(q))
	return protobuf.FromCheckChallengeMaterialsResponse(v), protobuf.FromError(err)
}
func (c Clients) OpenRunRegistration(ctx context.Context, q *dto.OpenRunRegistrationRequest) (*dto.OpenRunRegistrationResponse, error) {
	v, err := c.Content.OpenRunRegistration(ctx, protobuf.ToOpenRunRegistrationRequest(q))
	return protobuf.FromOpenRunRegistrationResponse(v), protobuf.FromError(err)
}
func (c Clients) CloseRunRegistration(ctx context.Context, q *dto.CloseRunRegistrationRequest) (*dto.CloseRunRegistrationResponse, error) {
	v, err := c.Content.CloseRunRegistration(ctx, protobuf.ToCloseRunRegistrationRequest(q))
	return protobuf.FromCloseRunRegistrationResponse(v), protobuf.FromError(err)
}
func (c Clients) RegisterChallengeCandidate(ctx context.Context, q *dto.RegisterChallengeCandidateRequest) (*dto.RegisterChallengeCandidateResponse, error) {
	v, err := c.Content.RegisterChallengeCandidate(ctx, protobuf.ToRegisterChallengeCandidateRequest(q))
	return protobuf.FromRegisterChallengeCandidateResponse(v), protobuf.FromError(err)
}
func (c Clients) GetChallengeRegistration(ctx context.Context, q *dto.GetChallengeRegistrationRequest) (*dto.GetChallengeRegistrationResponse, error) {
	v, err := c.Content.GetChallengeRegistration(ctx, protobuf.ToGetChallengeRegistrationRequest(q))
	return protobuf.FromGetChallengeRegistrationResponse(v), protobuf.FromError(err)
}
func (c Clients) GetAsset(ctx context.Context, q *dto.GetAssetRequest) (*dto.GetAssetResponse, error) {
	v, err := c.Asset.GetAsset(ctx, protobuf.ToGetAssetRequest(q))
	return protobuf.FromGetAssetResponse(v), protobuf.FromError(err)
}
func (c Clients) GetHumanPreferenceSnapshot(ctx context.Context, q *dto.GetHumanPreferenceSnapshotRequest) (*domain.PreferenceBatch, error) {
	v, err := c.Voting.GetHumanPreferenceSnapshot(ctx, protobuf.ToGetHumanPreferenceSnapshotRequest(q))
	return protobuf.FromPreferenceBatch(v), protobuf.FromError(err)
}
func (c Clients) CheckHumanPreferenceSnapshot(ctx context.Context, q *dto.CheckHumanPreferenceSnapshotRequest) (*dto.CheckHumanPreferenceSnapshotResponse, error) {
	v, err := c.Voting.CheckHumanPreferenceSnapshot(ctx, protobuf.ToCheckHumanPreferenceSnapshotRequest(q))
	return protobuf.FromCheckHumanPreferenceSnapshotResponse(v), protobuf.FromError(err)
}

type assetReader struct {
	grpc.ServerStreamingClient[asset.ReadAssetResponse]
}

func (r assetReader) Recv() (*dto.ReadAssetResponse, error) {
	v, err := r.ServerStreamingClient.Recv()
	return protobuf.FromReadAssetResponse(v), protobuf.FromError(err)
}
func (c Clients) ReadAsset(ctx context.Context, q *dto.ReadAssetRequest) (application.AssetReader, error) {
	v, err := c.Asset.ReadAsset(ctx, protobuf.ToReadAssetRequest(q))
	if err != nil {
		return nil, protobuf.FromError(err)
	}
	return assetReader{v}, nil
}

type assetWriter struct {
	grpc.ClientStreamingClient[asset.UploadAssetRequest, asset.UploadAssetResponse]
}

func (w assetWriter) Send(q *dto.UploadAssetRequest) error {
	return protobuf.FromError(w.ClientStreamingClient.Send(protobuf.ToUploadAssetRequest(q)))
}
func (w assetWriter) CloseAndRecv() (*dto.UploadAssetResponse, error) {
	v, err := w.ClientStreamingClient.CloseAndRecv()
	return protobuf.FromUploadAssetResponse(v), protobuf.FromError(err)
}
func (c Clients) UploadAsset(ctx context.Context) (application.AssetWriter, error) {
	v, err := c.Asset.UploadAsset(ctx)
	if err != nil {
		return nil, protobuf.FromError(err)
	}
	return assetWriter{v}, nil
}
