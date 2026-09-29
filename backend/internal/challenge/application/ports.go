package application

import (
	"context"
	"errors"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

var ErrNotFound = errors.New("record not found")

// Record is one database snapshot: the aggregate plus its fencing version and DB clock.
type Record struct {
	*domain.State
	Version       int64
	Worker        string
	Now, Deadline time.Time
}
type RunKey struct{ Administrator, Operation, Key, Hash string }
type ClaimReceipt struct{ RunID, AttemptID, Hash string }
type ResultReceipt struct {
	Digest, Worker, WorkID string
	Epoch                  int64
}
type ModelCall struct{ ID, State, Digest string }

type Reader interface {
	Read(context.Context, string) (*Record, error)
	LookupRun(context.Context, RunKey) (*domain.Run, error)
	ListRuns(context.Context, string, string, int32) ([]*domain.Run, error)
	Summary(context.Context) (*dto.GetRunSummaryResponse, error)
	PendingRuns(context.Context) ([]string, error)
}

// All methods of a Tx use the same connection and transaction. Application owns
// the unit of work; repositories own SQL, locking and serialization.
type Tx interface {
	Now(context.Context) (time.Time, error)
	Load(context.Context, string) (*Record, error)
	Save(context.Context, *Record) error
	InsertRun(context.Context, RunKey, *domain.State, time.Time) (bool, error)
	Audit(context.Context, string, string, string, string) error
	LockClaims(context.Context) error
	FindClaim(context.Context, string, string) (ClaimReceipt, error)
	LeaseUsage(context.Context, string) (int, bool, error)
	NextRun(context.Context, []int32) (string, error)
	AddClaim(context.Context, string, string, ClaimReceipt) error
	FindModelCall(context.Context, string, string) (ModelCall, error)
	ModelCallCounts(context.Context, string) (int, int, error)
	AddModelCall(context.Context, *Record, *dto.ReserveModelCallRequest, string) error
	LockModelCall(context.Context, *dto.SettleModelCallRequest, string) (string, error)
	SettleModelCall(context.Context, string, string) error
	FindResult(context.Context, *domain.AttemptRef) (ResultReceipt, error)
	AddResult(context.Context, *domain.AttemptRef, string, string) error
}
type Transactions interface {
	WithinTx(context.Context, func(Tx) error) error
}

type Administrator interface {
	Verify(context.Context, string, string) (string, error)
}
type Content interface {
	GetChallengeTaskDescription(context.Context, *dto.GetChallengeTaskDescriptionRequest) (*dto.GetChallengeTaskDescriptionResponse, error)
	ListChallengeWorks(context.Context, *dto.ListChallengeWorksRequest) (*dto.ListChallengeWorksResponse, error)
	ListChallengeComments(context.Context, *dto.ListChallengeCommentsRequest) (*dto.ListChallengeCommentsResponse, error)
	CheckChallengeMaterials(context.Context, *dto.CheckChallengeMaterialsRequest) (*dto.CheckChallengeMaterialsResponse, error)
	OpenRunRegistration(context.Context, *dto.OpenRunRegistrationRequest) (*dto.OpenRunRegistrationResponse, error)
	CloseRunRegistration(context.Context, *dto.CloseRunRegistrationRequest) (*dto.CloseRunRegistrationResponse, error)
	RegisterChallengeCandidate(context.Context, *dto.RegisterChallengeCandidateRequest) (*dto.RegisterChallengeCandidateResponse, error)
	GetChallengeRegistration(context.Context, *dto.GetChallengeRegistrationRequest) (*dto.GetChallengeRegistrationResponse, error)
}
type Voting interface {
	GetHumanPreferenceSnapshot(context.Context, *dto.GetHumanPreferenceSnapshotRequest) (*domain.PreferenceBatch, error)
	CheckHumanPreferenceSnapshot(context.Context, *dto.CheckHumanPreferenceSnapshotRequest) (*dto.CheckHumanPreferenceSnapshotResponse, error)
}
type AssetReader interface {
	Recv() (*dto.ReadAssetResponse, error)
}
type AssetWriter interface {
	Send(*dto.UploadAssetRequest) error
	CloseAndRecv() (*dto.UploadAssetResponse, error)
}
type Asset interface {
	GetAsset(context.Context, *dto.GetAssetRequest) (*dto.GetAssetResponse, error)
	ReadAsset(context.Context, *dto.ReadAssetRequest) (AssetReader, error)
	UploadAsset(context.Context) (AssetWriter, error)
}

// Fingerprints preserve the existing wire-byte hashing and size limits across a
// rolling upgrade without letting protobuf types enter the application/domain.
type Fingerprints interface {
	Configuration(*domain.RunConfiguration) string
	Model(*domain.ModelConfiguration) string
	Executor(*domain.ExecutorConfiguration) string
	Claim(*dto.ClaimWorkRequest) string
	Result(*dto.CompleteWorkRequest) string
	SampleSize(*domain.RankingSample) int
	RefineSize(*domain.RefineRankerInput) int
	PreferenceSize(*domain.HumanPreference) int
}
type MaterialStream interface {
	Context() context.Context
	Send(*dto.ReadMaterialResponse) error
}
type UploadStream interface {
	Context() context.Context
	Recv() (*dto.UploadArtifactRequest, error)
	SendAndClose(*dto.UploadArtifactResponse) error
}

type workerKey struct{}

// WithWorkerCertificate is used by an authenticated transport, never a request field.
func WithWorkerCertificate(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, workerKey{}, fingerprint)
}
func workerIdentity(ctx context.Context, instance string) (string, error) {
	fingerprint, _ := ctx.Value(workerKey{}).(string)
	if !domain.ValidDigest(fingerprint) || !domain.ValidID(instance) {
		return "", domain.Denied("worker_identity_required")
	}
	return fingerprint + "/" + instance, nil
}
