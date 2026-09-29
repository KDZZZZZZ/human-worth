package dto

import (
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
)

type StartRunRequest struct {
	ActorAssertion string
	TaskId         string
	Configuration  *domain.RunConfiguration
	IdempotencyKey string
}

func (v *StartRunRequest) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type StartRunResponse struct {
	Run *domain.Run
}

func (v *StartRunResponse) GetRun() *domain.Run {
	if v != nil {
		return v.Run
	}
	return nil
}

type GetRunRequest struct {
	ActorAssertion string
	RunId          string
}

type GetRunResponse struct {
	Run *domain.Run
}

func (v *GetRunResponse) GetRun() *domain.Run {
	if v != nil {
		return v.Run
	}
	return nil
}

type ListRunsRequest struct {
	ActorAssertion string
	Cursor         string
	Limit          int32
	Status         string
}

type ListRunsResponse struct {
	Items      []*domain.Run
	NextCursor string
}

type GetRunSummaryRequest struct {
	ActorAssertion string
}

type GetRunSummaryResponse struct {
	ActiveRuns int64
	FailedRuns int64
}

type CancelRunRequest struct {
	ActorAssertion string
	RunId          string
	Reason         string
}

type CancelRunResponse struct {
	Run *domain.Run
}

func (v *CancelRunResponse) GetRun() *domain.Run {
	if v != nil {
		return v.Run
	}
	return nil
}

type RestartRunRequest struct {
	ActorAssertion string
	ParentRunId    string
	Configuration  *domain.RunConfiguration
	IdempotencyKey string
}

type RestartRunResponse struct {
	Run *domain.Run
}

func (v *RestartRunResponse) GetRun() *domain.Run {
	if v != nil {
		return v.Run
	}
	return nil
}

type RegisterCandidateRequest struct {
	ActorAssertion string
	RunId          string
	CandidateId    string
}

type RegisterCandidateResponse struct {
	Receipt *domain.RegistrationReceipt
}

type ClaimWorkRequest struct {
	WorkerInstanceId string
	ClaimRequestId   string
	Capabilities     []domain.WorkKind
}

type ClaimWorkResponse struct {
	Assignment *domain.Assignment
}

type RenewLeaseRequest struct {
	Attempt *domain.AttemptRef
}

type RenewLeaseResponse struct {
	ExpiresAt     *time.Time
	StopRequested bool
}

type ReportProgressRequest struct {
	Attempt  *domain.AttemptRef
	Sequence int64
	Phase    string
}

type ReportProgressResponse struct {
	AckSequence int64
}

type ReadMaterialRequest struct {
	Attempt    *domain.AttemptRef
	MaterialId string
	Offset     int64
}

type ReadMaterialResponse struct {
	Chunk  []byte
	Offset int64
	Digest string
}

type UploadArtifactRequest struct {
	Attempt   *domain.AttemptRef
	UploadId  string
	Filename  string
	MediaType string
	Bytes     int64
	Sha256    string
	Chunk     []byte
}

func (v *UploadArtifactRequest) GetSha256() string {
	if v != nil {
		return v.Sha256
	}
	return ""
}

type UploadArtifactResponse struct {
	Asset *domain.Asset
}

func (v *UploadArtifactResponse) GetAsset() *domain.Asset {
	if v != nil {
		return v.Asset
	}
	return nil
}

type CompleteWorkRequest struct {
	Attempt      *domain.AttemptRef
	ResultDigest string
	Result       isCompleteWorkRequest_Result
}

type isCompleteWorkRequest_Result interface{ isCompleteWorkRequest_Result() }
type CompleteWorkRequest_Prompt struct{ Prompt *domain.PromptResult }

func (*CompleteWorkRequest_Prompt) isCompleteWorkRequest_Result() {}

type CompleteWorkRequest_Rankings struct{ Rankings *domain.Rankings }

func (*CompleteWorkRequest_Rankings) isCompleteWorkRequest_Result() {}

type CompleteWorkRequest_Generated struct{ Generated *domain.GeneratedWork }

func (*CompleteWorkRequest_Generated) isCompleteWorkRequest_Result() {}

type CompleteWorkRequest_Judgment struct{ Judgment *domain.Judgment }

func (*CompleteWorkRequest_Judgment) isCompleteWorkRequest_Result() {}
func (v *CompleteWorkRequest) GetPrompt() *domain.PromptResult {
	if v != nil {
		if x, ok := v.Result.(*CompleteWorkRequest_Prompt); ok {
			return x.Prompt
		}
	}
	return nil
}
func (v *CompleteWorkRequest) GetRankings() *domain.Rankings {
	if v != nil {
		if x, ok := v.Result.(*CompleteWorkRequest_Rankings); ok {
			return x.Rankings
		}
	}
	return nil
}
func (v *CompleteWorkRequest) GetGenerated() *domain.GeneratedWork {
	if v != nil {
		if x, ok := v.Result.(*CompleteWorkRequest_Generated); ok {
			return x.Generated
		}
	}
	return nil
}
func (v *CompleteWorkRequest) GetJudgment() *domain.Judgment {
	if v != nil {
		if x, ok := v.Result.(*CompleteWorkRequest_Judgment); ok {
			return x.Judgment
		}
	}
	return nil
}

type CompleteWorkResponse struct {
	Accepted bool
}

type FailWorkRequest struct {
	Attempt      *domain.AttemptRef
	FailureCode  string
	OutcomeKnown bool
}

type FailWorkResponse struct {
	Accepted bool
}

type ActivateAttemptRequest struct {
	Attempt             *domain.AttemptRef
	ExecutionInstanceId string
}

type ActivateAttemptResponse struct {
	Grant     string
	ExpiresAt *time.Time
}

type CheckTaskAccessRequest struct {
	Attempt     *domain.AttemptRef
	Grant       string
	Operation   string
	ResourceRef string
}

type CheckTaskAccessResponse struct {
	Allowed   bool
	ExpiresAt *time.Time
}

type ReserveModelCallRequest struct {
	Attempt       *domain.AttemptRef
	Grant         string
	RequestId     string
	RequestDigest string
	Model         string
}

func (v *ReserveModelCallRequest) GetModel() string {
	if v != nil {
		return v.Model
	}
	return ""
}

type ReserveModelCallResponse struct {
	ReservationId  string
	State          string
	DispatchPermit bool
}

type SettleModelCallRequest struct {
	Attempt       *domain.AttemptRef
	ReservationId string
	Outcome       string
}

type SettleModelCallResponse struct {
	State string
}

type GetChallengeTaskDescriptionRequest struct {
	Snapshot *domain.SnapshotRef
}

type GetChallengeTaskDescriptionResponse struct {
	Task    *domain.TaskDescription
	Visible bool
}

type ListChallengeWorksRequest struct {
	Snapshot *domain.SnapshotRef
}

type ListChallengeWorksResponse struct {
	Works           []*domain.Work
	CatalogRevision int64
}

type ListChallengeCommentsRequest struct {
	Snapshot *domain.SnapshotRef
}

type ListChallengeCommentsResponse struct {
	Comments []*domain.Comment
	CutoffAt *time.Time
}

type CheckChallengeMaterialsRequest struct {
	RunId     string
	Snapshot  *domain.SnapshotRef
	Purpose   string
	WorkIds   []string
	Materials []*domain.MaterialRef
}

type CheckChallengeMaterialsResponse struct {
	Allowed              bool
	AuthorizationVersion string
}

type OpenRunRegistrationRequest struct {
	RunId  string
	TaskId string
}

func (v *OpenRunRegistrationRequest) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type OpenRunRegistrationResponse struct {
	State string
}

type CloseRunRegistrationRequest struct {
	RunId  string
	TaskId string
}

func (v *CloseRunRegistrationRequest) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type CloseRunRegistrationResponse struct {
	State string
}

type RegisterChallengeCandidateRequest struct {
	RunId              string
	CandidateId        string
	TaskId             string
	SourceAttemptId    string
	Work               *domain.Work
	ExecutorConfigHash string
}

func (v *RegisterChallengeCandidateRequest) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}
func (v *RegisterChallengeCandidateRequest) GetWork() *domain.Work {
	if v != nil {
		return v.Work
	}
	return nil
}

type RegisterChallengeCandidateResponse struct {
	Receipt *domain.RegistrationReceipt
}

type GetChallengeRegistrationRequest struct {
	RunId       string
	CandidateId string
}

type GetChallengeRegistrationResponse struct {
	Receipt *domain.RegistrationReceipt
}

type GetAssetRequest struct {
	AssetId string
	Access  *domain.Access
}

type GetAssetResponse struct {
	Asset *domain.Asset
}

func (v *GetAssetResponse) GetAsset() *domain.Asset {
	if v != nil {
		return v.Asset
	}
	return nil
}

type ReadAssetRequest struct {
	AssetId string
	Access  *domain.Access
	Offset  int64
}

type ReadAssetResponse struct {
	Chunk  []byte
	Offset int64
	Sha256 string
}

func (v *ReadAssetResponse) GetSha256() string {
	if v != nil {
		return v.Sha256
	}
	return ""
}

type UploadAssetRequest struct {
	Access    *domain.Access
	UploadId  string
	Filename  string
	MediaType string
	Bytes     int64
	Sha256    string
	Chunk     []byte
}

func (v *UploadAssetRequest) GetSha256() string {
	if v != nil {
		return v.Sha256
	}
	return ""
}

type UploadAssetResponse struct {
	Asset *domain.Asset
}

func (v *UploadAssetResponse) GetAsset() *domain.Asset {
	if v != nil {
		return v.Asset
	}
	return nil
}

type GetHumanPreferenceSnapshotRequest struct {
	RunId           string
	Purpose         string
	ExcludedTaskIds []string
	BatchIndex      int32
}

type CheckHumanPreferenceSnapshotRequest struct {
	SnapshotId    string
	PolicyVersion string
}

func (v *CheckHumanPreferenceSnapshotRequest) GetSnapshotId() string {
	if v != nil {
		return v.SnapshotId
	}
	return ""
}
func (v *CheckHumanPreferenceSnapshotRequest) GetPolicyVersion() string {
	if v != nil {
		return v.PolicyVersion
	}
	return ""
}

type CheckHumanPreferenceSnapshotResponse struct {
	Valid bool
}
