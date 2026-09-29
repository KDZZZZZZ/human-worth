package protobuf

import (
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	challenge "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func fromTime(v *timestamppb.Timestamp) *time.Time {
	if v == nil {
		return nil
	}
	t := v.AsTime()
	if v.CheckValid() != nil {
		t = time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC)
	}
	return &t
}
func toTime(v *time.Time) *timestamppb.Timestamp {
	if v == nil {
		return nil
	}
	return timestamppb.New(*v)
}
func fromParameters(v *structpb.Struct) map[string]any {
	if v == nil {
		return nil
	}
	return v.AsMap()
}
func toParameters(v map[string]any) *structpb.Struct {
	if v == nil {
		return nil
	}
	s, _ := structpb.NewStruct(v)
	return s
}
func FromInitialTaskPackage(v *challenge.InitialTaskPackage) *domain.InitialTaskPackage {
	if v == nil {
		return nil
	}
	out := &domain.InitialTaskPackage{}
	out.TaskRevision = v.TaskRevision
	out.TaskAttachmentIds = append(out.TaskAttachmentIds, v.TaskAttachmentIds...)
	out.Description = v.Description
	out.WorkRequirements = v.WorkRequirements
	return out
}
func ToInitialTaskPackage(v *domain.InitialTaskPackage) *challenge.InitialTaskPackage {
	if v == nil {
		return nil
	}
	out := &challenge.InitialTaskPackage{}
	out.TaskRevision = v.TaskRevision
	out.TaskAttachmentIds = append(out.TaskAttachmentIds, v.TaskAttachmentIds...)
	out.Description = v.Description
	out.WorkRequirements = v.WorkRequirements
	return out
}
func FromModelConfiguration(v *challenge.ModelConfiguration) *domain.ModelConfiguration {
	if v == nil {
		return nil
	}
	out := &domain.ModelConfiguration{}
	out.Model = v.Model
	out.Prompt = v.Prompt
	out.Parameters = fromParameters(v.Parameters)
	return out
}
func ToModelConfiguration(v *domain.ModelConfiguration) *challenge.ModelConfiguration {
	if v == nil {
		return nil
	}
	out := &challenge.ModelConfiguration{}
	out.Model = v.Model
	out.Prompt = v.Prompt
	out.Parameters = toParameters(v.Parameters)
	return out
}
func FromExecutorConfiguration(v *challenge.ExecutorConfiguration) *domain.ExecutorConfiguration {
	if v == nil {
		return nil
	}
	out := &domain.ExecutorConfiguration{}
	out.Model = v.Model
	out.Prompt = v.Prompt
	out.Harness = v.Harness
	out.Skills = append(out.Skills, v.Skills...)
	out.Parameters = fromParameters(v.Parameters)
	return out
}
func ToExecutorConfiguration(v *domain.ExecutorConfiguration) *challenge.ExecutorConfiguration {
	if v == nil {
		return nil
	}
	out := &challenge.ExecutorConfiguration{}
	out.Model = v.Model
	out.Prompt = v.Prompt
	out.Harness = v.Harness
	out.Skills = append(out.Skills, v.Skills...)
	out.Parameters = toParameters(v.Parameters)
	return out
}
func FromBudget(v *challenge.Budget) *domain.Budget {
	if v == nil {
		return nil
	}
	out := &domain.Budget{}
	out.Amount = v.Amount
	out.Unit = v.Unit
	return out
}
func ToBudget(v *domain.Budget) *challenge.Budget {
	if v == nil {
		return nil
	}
	out := &challenge.Budget{}
	out.Amount = v.Amount
	out.Unit = v.Unit
	return out
}
func FromRunConfiguration(v *challenge.RunConfiguration) *domain.RunConfiguration {
	if v == nil {
		return nil
	}
	out := &domain.RunConfiguration{}
	out.InitialTaskPackage = FromInitialTaskPackage(v.InitialTaskPackage)
	out.Packer = FromModelConfiguration(v.Packer)
	out.Executor = FromExecutorConfiguration(v.Executor)
	out.Ranker = FromModelConfiguration(v.Ranker)
	out.RankerFitThreshold = v.RankerFitThreshold
	out.RankerMinComparablePairs = v.RankerMinComparablePairs
	out.RankerRoundLimit = v.RankerRoundLimit
	out.RoundLimit = v.RoundLimit
	out.Budget = FromBudget(v.Budget)
	return out
}
func ToRunConfiguration(v *domain.RunConfiguration) *challenge.RunConfiguration {
	if v == nil {
		return nil
	}
	out := &challenge.RunConfiguration{}
	out.InitialTaskPackage = ToInitialTaskPackage(v.InitialTaskPackage)
	out.Packer = ToModelConfiguration(v.Packer)
	out.Executor = ToExecutorConfiguration(v.Executor)
	out.Ranker = ToModelConfiguration(v.Ranker)
	out.RankerFitThreshold = v.RankerFitThreshold
	out.RankerMinComparablePairs = v.RankerMinComparablePairs
	out.RankerRoundLimit = v.RankerRoundLimit
	out.RoundLimit = v.RoundLimit
	out.Budget = ToBudget(v.Budget)
	return out
}
func FromRankerFit(v *challenge.RankerFit) *domain.RankerFit {
	if v == nil {
		return nil
	}
	out := &domain.RankerFit{}
	out.Status = v.Status
	out.Score = v.Score
	out.EvaluatedAt = fromTime(v.EvaluatedAt)
	return out
}
func ToRankerFit(v *domain.RankerFit) *challenge.RankerFit {
	if v == nil {
		return nil
	}
	out := &challenge.RankerFit{}
	out.Status = v.Status
	out.Score = v.Score
	out.EvaluatedAt = toTime(v.EvaluatedAt)
	return out
}
func FromCandidate(v *challenge.Candidate) *domain.Candidate {
	if v == nil {
		return nil
	}
	out := &domain.Candidate{}
	out.Id = v.Id
	out.RegistrationState = v.RegistrationState
	out.EntryId = v.EntryId
	out.ReviewId = v.ReviewId
	return out
}
func ToCandidate(v *domain.Candidate) *challenge.Candidate {
	if v == nil {
		return nil
	}
	out := &challenge.Candidate{}
	out.Id = v.Id
	out.RegistrationState = v.RegistrationState
	out.EntryId = v.EntryId
	out.ReviewId = v.ReviewId
	return out
}
func FromRun(v *challenge.Run) *domain.Run {
	if v == nil {
		return nil
	}
	out := &domain.Run{}
	out.Id = v.Id
	out.TaskId = v.TaskId
	out.ParentRunId = v.ParentRunId
	out.AdministratorId = v.AdministratorId
	out.Status = v.Status
	out.Stage = v.Stage
	out.RankerRound = v.RankerRound
	out.Round = v.Round
	out.RankerFit = FromRankerFit(v.RankerFit)
	out.Configuration = FromRunConfiguration(v.Configuration)
	out.FailureReason = v.FailureReason
	for _, x := range v.Candidates {
		out.Candidates = append(out.Candidates, FromCandidate(x))
	}
	out.CreatedAt = fromTime(v.CreatedAt)
	out.UpdatedAt = fromTime(v.UpdatedAt)
	out.EndedAt = fromTime(v.EndedAt)
	return out
}
func ToRun(v *domain.Run) *challenge.Run {
	if v == nil {
		return nil
	}
	out := &challenge.Run{}
	out.Id = v.Id
	out.TaskId = v.TaskId
	out.ParentRunId = v.ParentRunId
	out.AdministratorId = v.AdministratorId
	out.Status = v.Status
	out.Stage = v.Stage
	out.RankerRound = v.RankerRound
	out.Round = v.Round
	out.RankerFit = ToRankerFit(v.RankerFit)
	out.Configuration = ToRunConfiguration(v.Configuration)
	out.FailureReason = v.FailureReason
	for _, x := range v.Candidates {
		out.Candidates = append(out.Candidates, ToCandidate(x))
	}
	out.CreatedAt = toTime(v.CreatedAt)
	out.UpdatedAt = toTime(v.UpdatedAt)
	out.EndedAt = toTime(v.EndedAt)
	return out
}
func FromStartRunRequest(v *challenge.StartRunRequest) *dto.StartRunRequest {
	if v == nil {
		return nil
	}
	out := &dto.StartRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.TaskId = v.TaskId
	out.Configuration = FromRunConfiguration(v.Configuration)
	out.IdempotencyKey = v.IdempotencyKey
	return out
}
func ToStartRunRequest(v *dto.StartRunRequest) *challenge.StartRunRequest {
	if v == nil {
		return nil
	}
	out := &challenge.StartRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.TaskId = v.TaskId
	out.Configuration = ToRunConfiguration(v.Configuration)
	out.IdempotencyKey = v.IdempotencyKey
	return out
}
func FromStartRunResponse(v *challenge.StartRunResponse) *dto.StartRunResponse {
	if v == nil {
		return nil
	}
	out := &dto.StartRunResponse{}
	out.Run = FromRun(v.Run)
	return out
}
func ToStartRunResponse(v *dto.StartRunResponse) *challenge.StartRunResponse {
	if v == nil {
		return nil
	}
	out := &challenge.StartRunResponse{}
	out.Run = ToRun(v.Run)
	return out
}
func FromGetRunRequest(v *challenge.GetRunRequest) *dto.GetRunRequest {
	if v == nil {
		return nil
	}
	out := &dto.GetRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.RunId = v.RunId
	return out
}
func ToGetRunRequest(v *dto.GetRunRequest) *challenge.GetRunRequest {
	if v == nil {
		return nil
	}
	out := &challenge.GetRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.RunId = v.RunId
	return out
}
func FromGetRunResponse(v *challenge.GetRunResponse) *dto.GetRunResponse {
	if v == nil {
		return nil
	}
	out := &dto.GetRunResponse{}
	out.Run = FromRun(v.Run)
	return out
}
func ToGetRunResponse(v *dto.GetRunResponse) *challenge.GetRunResponse {
	if v == nil {
		return nil
	}
	out := &challenge.GetRunResponse{}
	out.Run = ToRun(v.Run)
	return out
}
func FromListRunsRequest(v *challenge.ListRunsRequest) *dto.ListRunsRequest {
	if v == nil {
		return nil
	}
	out := &dto.ListRunsRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.Cursor = v.Cursor
	out.Limit = v.Limit
	out.Status = v.Status
	return out
}
func ToListRunsRequest(v *dto.ListRunsRequest) *challenge.ListRunsRequest {
	if v == nil {
		return nil
	}
	out := &challenge.ListRunsRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.Cursor = v.Cursor
	out.Limit = v.Limit
	out.Status = v.Status
	return out
}
func FromListRunsResponse(v *challenge.ListRunsResponse) *dto.ListRunsResponse {
	if v == nil {
		return nil
	}
	out := &dto.ListRunsResponse{}
	for _, x := range v.Items {
		out.Items = append(out.Items, FromRun(x))
	}
	out.NextCursor = v.NextCursor
	return out
}
func ToListRunsResponse(v *dto.ListRunsResponse) *challenge.ListRunsResponse {
	if v == nil {
		return nil
	}
	out := &challenge.ListRunsResponse{}
	for _, x := range v.Items {
		out.Items = append(out.Items, ToRun(x))
	}
	out.NextCursor = v.NextCursor
	return out
}
func FromGetRunSummaryRequest(v *challenge.GetRunSummaryRequest) *dto.GetRunSummaryRequest {
	if v == nil {
		return nil
	}
	out := &dto.GetRunSummaryRequest{}
	out.ActorAssertion = v.ActorAssertion
	return out
}
func ToGetRunSummaryRequest(v *dto.GetRunSummaryRequest) *challenge.GetRunSummaryRequest {
	if v == nil {
		return nil
	}
	out := &challenge.GetRunSummaryRequest{}
	out.ActorAssertion = v.ActorAssertion
	return out
}
func FromGetRunSummaryResponse(v *challenge.GetRunSummaryResponse) *dto.GetRunSummaryResponse {
	if v == nil {
		return nil
	}
	out := &dto.GetRunSummaryResponse{}
	out.ActiveRuns = v.ActiveRuns
	out.FailedRuns = v.FailedRuns
	return out
}
func ToGetRunSummaryResponse(v *dto.GetRunSummaryResponse) *challenge.GetRunSummaryResponse {
	if v == nil {
		return nil
	}
	out := &challenge.GetRunSummaryResponse{}
	out.ActiveRuns = v.ActiveRuns
	out.FailedRuns = v.FailedRuns
	return out
}
func FromCancelRunRequest(v *challenge.CancelRunRequest) *dto.CancelRunRequest {
	if v == nil {
		return nil
	}
	out := &dto.CancelRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.RunId = v.RunId
	out.Reason = v.Reason
	return out
}
func ToCancelRunRequest(v *dto.CancelRunRequest) *challenge.CancelRunRequest {
	if v == nil {
		return nil
	}
	out := &challenge.CancelRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.RunId = v.RunId
	out.Reason = v.Reason
	return out
}
func FromCancelRunResponse(v *challenge.CancelRunResponse) *dto.CancelRunResponse {
	if v == nil {
		return nil
	}
	out := &dto.CancelRunResponse{}
	out.Run = FromRun(v.Run)
	return out
}
func ToCancelRunResponse(v *dto.CancelRunResponse) *challenge.CancelRunResponse {
	if v == nil {
		return nil
	}
	out := &challenge.CancelRunResponse{}
	out.Run = ToRun(v.Run)
	return out
}
func FromRestartRunRequest(v *challenge.RestartRunRequest) *dto.RestartRunRequest {
	if v == nil {
		return nil
	}
	out := &dto.RestartRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.ParentRunId = v.ParentRunId
	out.Configuration = FromRunConfiguration(v.Configuration)
	out.IdempotencyKey = v.IdempotencyKey
	return out
}
func ToRestartRunRequest(v *dto.RestartRunRequest) *challenge.RestartRunRequest {
	if v == nil {
		return nil
	}
	out := &challenge.RestartRunRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.ParentRunId = v.ParentRunId
	out.Configuration = ToRunConfiguration(v.Configuration)
	out.IdempotencyKey = v.IdempotencyKey
	return out
}
func FromRestartRunResponse(v *challenge.RestartRunResponse) *dto.RestartRunResponse {
	if v == nil {
		return nil
	}
	out := &dto.RestartRunResponse{}
	out.Run = FromRun(v.Run)
	return out
}
func ToRestartRunResponse(v *dto.RestartRunResponse) *challenge.RestartRunResponse {
	if v == nil {
		return nil
	}
	out := &challenge.RestartRunResponse{}
	out.Run = ToRun(v.Run)
	return out
}
func FromRegisterCandidateRequest(v *challenge.RegisterCandidateRequest) *dto.RegisterCandidateRequest {
	if v == nil {
		return nil
	}
	out := &dto.RegisterCandidateRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	return out
}
func ToRegisterCandidateRequest(v *dto.RegisterCandidateRequest) *challenge.RegisterCandidateRequest {
	if v == nil {
		return nil
	}
	out := &challenge.RegisterCandidateRequest{}
	out.ActorAssertion = v.ActorAssertion
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	return out
}
func FromRegisterCandidateResponse(v *challenge.RegisterCandidateResponse) *dto.RegisterCandidateResponse {
	if v == nil {
		return nil
	}
	out := &dto.RegisterCandidateResponse{}
	out.Receipt = FromRegistrationReceipt(v.Receipt)
	return out
}
func ToRegisterCandidateResponse(v *dto.RegisterCandidateResponse) *challenge.RegisterCandidateResponse {
	if v == nil {
		return nil
	}
	out := &challenge.RegisterCandidateResponse{}
	out.Receipt = ToRegistrationReceipt(v.Receipt)
	return out
}
func FromAttemptRef(v *challenge.AttemptRef) *domain.AttemptRef {
	if v == nil {
		return nil
	}
	out := &domain.AttemptRef{}
	out.RunId = v.RunId
	out.WorkItemId = v.WorkItemId
	out.AttemptId = v.AttemptId
	out.LeaseEpoch = v.LeaseEpoch
	out.WorkerInstanceId = v.WorkerInstanceId
	return out
}
func ToAttemptRef(v *domain.AttemptRef) *challenge.AttemptRef {
	if v == nil {
		return nil
	}
	out := &challenge.AttemptRef{}
	out.RunId = v.RunId
	out.WorkItemId = v.WorkItemId
	out.AttemptId = v.AttemptId
	out.LeaseEpoch = v.LeaseEpoch
	out.WorkerInstanceId = v.WorkerInstanceId
	return out
}
func FromJudgment(v *challenge.Judgment) *domain.Judgment {
	if v == nil {
		return nil
	}
	out := &domain.Judgment{}
	out.WorkId = v.WorkId
	out.Criteria = v.Criteria
	out.Rationale = v.Rationale
	out.EvidenceRefs = append(out.EvidenceRefs, v.EvidenceRefs...)
	return out
}
func ToJudgment(v *domain.Judgment) *challenge.Judgment {
	if v == nil {
		return nil
	}
	out := &challenge.Judgment{}
	out.WorkId = v.WorkId
	out.Criteria = v.Criteria
	out.Rationale = v.Rationale
	out.EvidenceRefs = append(out.EvidenceRefs, v.EvidenceRefs...)
	return out
}
func FromRanking(v *challenge.Ranking) *domain.Ranking {
	if v == nil {
		return nil
	}
	out := &domain.Ranking{}
	out.TaskId = v.TaskId
	out.OrderedWorkIds = append(out.OrderedWorkIds, v.OrderedWorkIds...)
	for _, x := range v.Judgments {
		out.Judgments = append(out.Judgments, FromJudgment(x))
	}
	return out
}
func ToRanking(v *domain.Ranking) *challenge.Ranking {
	if v == nil {
		return nil
	}
	out := &challenge.Ranking{}
	out.TaskId = v.TaskId
	out.OrderedWorkIds = append(out.OrderedWorkIds, v.OrderedWorkIds...)
	for _, x := range v.Judgments {
		out.Judgments = append(out.Judgments, ToJudgment(x))
	}
	return out
}
func FromRankings(v *challenge.Rankings) *domain.Rankings {
	if v == nil {
		return nil
	}
	out := &domain.Rankings{}
	for _, x := range v.Items {
		out.Items = append(out.Items, FromRanking(x))
	}
	return out
}
func ToRankings(v *domain.Rankings) *challenge.Rankings {
	if v == nil {
		return nil
	}
	out := &challenge.Rankings{}
	for _, x := range v.Items {
		out.Items = append(out.Items, ToRanking(x))
	}
	return out
}
func FromRankingSample(v *challenge.RankingSample) *domain.RankingSample {
	if v == nil {
		return nil
	}
	out := &domain.RankingSample{}
	out.Task = FromTaskDescription(v.Task)
	for _, x := range v.Works {
		out.Works = append(out.Works, FromWork(x))
	}
	for _, x := range v.Comments {
		out.Comments = append(out.Comments, FromComment(x))
	}
	return out
}
func ToRankingSample(v *domain.RankingSample) *challenge.RankingSample {
	if v == nil {
		return nil
	}
	out := &challenge.RankingSample{}
	out.Task = ToTaskDescription(v.Task)
	for _, x := range v.Works {
		out.Works = append(out.Works, ToWork(x))
	}
	for _, x := range v.Comments {
		out.Comments = append(out.Comments, ToComment(x))
	}
	return out
}
func FromRankInput(v *challenge.RankInput) *domain.RankInput {
	if v == nil {
		return nil
	}
	out := &domain.RankInput{}
	for _, x := range v.Samples {
		out.Samples = append(out.Samples, FromRankingSample(x))
	}
	return out
}
func ToRankInput(v *domain.RankInput) *challenge.RankInput {
	if v == nil {
		return nil
	}
	out := &challenge.RankInput{}
	for _, x := range v.Samples {
		out.Samples = append(out.Samples, ToRankingSample(x))
	}
	return out
}
func FromTrainingSample(v *challenge.TrainingSample) *domain.TrainingSample {
	if v == nil {
		return nil
	}
	out := &domain.TrainingSample{}
	out.Sample = FromRankingSample(v.Sample)
	out.Ranking = FromRanking(v.Ranking)
	out.HumanCounts = v.HumanCounts
	return out
}
func ToTrainingSample(v *domain.TrainingSample) *challenge.TrainingSample {
	if v == nil {
		return nil
	}
	out := &challenge.TrainingSample{}
	out.Sample = ToRankingSample(v.Sample)
	out.Ranking = ToRanking(v.Ranking)
	out.HumanCounts = v.HumanCounts
	return out
}
func FromRefineRankerInput(v *challenge.RefineRankerInput) *domain.RefineRankerInput {
	if v == nil {
		return nil
	}
	out := &domain.RefineRankerInput{}
	out.Target = FromRankingSample(v.Target)
	out.Initial = FromInitialTaskPackage(v.Initial)
	for _, x := range v.Samples {
		out.Samples = append(out.Samples, FromTrainingSample(x))
	}
	out.PreviousFit = v.PreviousFit
	return out
}
func ToRefineRankerInput(v *domain.RefineRankerInput) *challenge.RefineRankerInput {
	if v == nil {
		return nil
	}
	out := &challenge.RefineRankerInput{}
	out.Target = ToRankingSample(v.Target)
	out.Initial = ToInitialTaskPackage(v.Initial)
	for _, x := range v.Samples {
		out.Samples = append(out.Samples, ToTrainingSample(x))
	}
	out.PreviousFit = v.PreviousFit
	return out
}
func FromOwnFeedback(v *challenge.OwnFeedback) *domain.OwnFeedback {
	if v == nil {
		return nil
	}
	out := &domain.OwnFeedback{}
	out.Work = FromWork(v.Work)
	out.Report = v.Report
	out.Rank = v.Rank
	out.PopulationSize = v.PopulationSize
	out.Judgment = FromJudgment(v.Judgment)
	out.RankerFitScore = v.RankerFitScore
	out.EvaluationId = v.EvaluationId
	return out
}
func ToOwnFeedback(v *domain.OwnFeedback) *challenge.OwnFeedback {
	if v == nil {
		return nil
	}
	out := &challenge.OwnFeedback{}
	out.Work = ToWork(v.Work)
	out.Report = v.Report
	out.Rank = v.Rank
	out.PopulationSize = v.PopulationSize
	out.Judgment = ToJudgment(v.Judgment)
	out.RankerFitScore = v.RankerFitScore
	out.EvaluationId = v.EvaluationId
	return out
}
func FromPackInput(v *challenge.PackInput) *domain.PackInput {
	if v == nil {
		return nil
	}
	out := &domain.PackInput{}
	out.Initial = FromInitialTaskPackage(v.Initial)
	out.ExecutionHints = v.ExecutionHints
	out.Feedback = FromOwnFeedback(v.Feedback)
	return out
}
func ToPackInput(v *domain.PackInput) *challenge.PackInput {
	if v == nil {
		return nil
	}
	out := &challenge.PackInput{}
	out.Initial = ToInitialTaskPackage(v.Initial)
	out.ExecutionHints = v.ExecutionHints
	out.Feedback = ToOwnFeedback(v.Feedback)
	return out
}
func FromExecuteInput(v *challenge.ExecuteInput) *domain.ExecuteInput {
	if v == nil {
		return nil
	}
	out := &domain.ExecuteInput{}
	out.Initial = FromInitialTaskPackage(v.Initial)
	out.ExecutionHints = v.ExecutionHints
	return out
}
func ToExecuteInput(v *domain.ExecuteInput) *challenge.ExecuteInput {
	if v == nil {
		return nil
	}
	out := &challenge.ExecuteInput{}
	out.Initial = ToInitialTaskPackage(v.Initial)
	out.ExecutionHints = v.ExecutionHints
	return out
}
func FromExplainInput(v *challenge.ExplainInput) *domain.ExplainInput {
	if v == nil {
		return nil
	}
	out := &domain.ExplainInput{}
	out.TaskDescription = v.TaskDescription
	out.OwnWork = FromWork(v.OwnWork)
	return out
}
func ToExplainInput(v *domain.ExplainInput) *challenge.ExplainInput {
	if v == nil {
		return nil
	}
	out := &challenge.ExplainInput{}
	out.TaskDescription = v.TaskDescription
	out.OwnWork = ToWork(v.OwnWork)
	return out
}
func FromMaterial(v *challenge.Material) *domain.Material {
	if v == nil {
		return nil
	}
	out := &domain.Material{}
	out.Id = v.Id
	out.Asset = FromAsset(v.Asset)
	out.TaskId = v.TaskId
	out.SourceKind = v.SourceKind
	out.SourceId = v.SourceId
	return out
}
func ToMaterial(v *domain.Material) *challenge.Material {
	if v == nil {
		return nil
	}
	out := &challenge.Material{}
	out.Id = v.Id
	out.Asset = ToAsset(v.Asset)
	out.TaskId = v.TaskId
	out.SourceKind = v.SourceKind
	out.SourceId = v.SourceId
	return out
}
func FromAssignment(v *challenge.Assignment) *domain.Assignment {
	if v == nil {
		return nil
	}
	out := &domain.Assignment{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.Kind = domain.WorkKind(v.Kind)
	out.Role = v.Role
	out.Model = FromModelConfiguration(v.Model)
	out.Executor = FromExecutorConfiguration(v.Executor)
	for _, x := range v.Materials {
		out.Materials = append(out.Materials, FromMaterial(x))
	}
	out.LeaseExpiresAt = fromTime(v.LeaseExpiresAt)
	out.Deadline = fromTime(v.Deadline)
	out.ModelCallLimit = v.ModelCallLimit
	out.ToolCallLimit = v.ToolCallLimit
	out.InputPolicyHash = v.InputPolicyHash
	out.WorkItemId = v.WorkItemId
	if x, ok := v.Input.(*challenge.Assignment_Rank); ok {
		out.Input = &domain.Assignment_Rank{Rank: FromRankInput(x.Rank)}
	}
	if x, ok := v.Input.(*challenge.Assignment_Refine); ok {
		out.Input = &domain.Assignment_Refine{Refine: FromRefineRankerInput(x.Refine)}
	}
	if x, ok := v.Input.(*challenge.Assignment_Pack); ok {
		out.Input = &domain.Assignment_Pack{Pack: FromPackInput(x.Pack)}
	}
	if x, ok := v.Input.(*challenge.Assignment_Execute); ok {
		out.Input = &domain.Assignment_Execute{Execute: FromExecuteInput(x.Execute)}
	}
	if x, ok := v.Input.(*challenge.Assignment_Explain); ok {
		out.Input = &domain.Assignment_Explain{Explain: FromExplainInput(x.Explain)}
	}
	return out
}
func ToAssignment(v *domain.Assignment) *challenge.Assignment {
	if v == nil {
		return nil
	}
	out := &challenge.Assignment{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.Kind = challenge.WorkKind(v.Kind)
	out.Role = v.Role
	out.Model = ToModelConfiguration(v.Model)
	out.Executor = ToExecutorConfiguration(v.Executor)
	for _, x := range v.Materials {
		out.Materials = append(out.Materials, ToMaterial(x))
	}
	out.LeaseExpiresAt = toTime(v.LeaseExpiresAt)
	out.Deadline = toTime(v.Deadline)
	out.ModelCallLimit = v.ModelCallLimit
	out.ToolCallLimit = v.ToolCallLimit
	out.InputPolicyHash = v.InputPolicyHash
	out.WorkItemId = v.WorkItemId
	if x, ok := v.Input.(*domain.Assignment_Rank); ok {
		out.Input = &challenge.Assignment_Rank{Rank: ToRankInput(x.Rank)}
	}
	if x, ok := v.Input.(*domain.Assignment_Refine); ok {
		out.Input = &challenge.Assignment_Refine{Refine: ToRefineRankerInput(x.Refine)}
	}
	if x, ok := v.Input.(*domain.Assignment_Pack); ok {
		out.Input = &challenge.Assignment_Pack{Pack: ToPackInput(x.Pack)}
	}
	if x, ok := v.Input.(*domain.Assignment_Execute); ok {
		out.Input = &challenge.Assignment_Execute{Execute: ToExecuteInput(x.Execute)}
	}
	if x, ok := v.Input.(*domain.Assignment_Explain); ok {
		out.Input = &challenge.Assignment_Explain{Explain: ToExplainInput(x.Explain)}
	}
	return out
}
func FromClaimWorkRequest(v *challenge.ClaimWorkRequest) *dto.ClaimWorkRequest {
	if v == nil {
		return nil
	}
	out := &dto.ClaimWorkRequest{}
	out.WorkerInstanceId = v.WorkerInstanceId
	out.ClaimRequestId = v.ClaimRequestId
	for _, x := range v.Capabilities {
		out.Capabilities = append(out.Capabilities, domain.WorkKind(x))
	}
	return out
}
func ToClaimWorkRequest(v *dto.ClaimWorkRequest) *challenge.ClaimWorkRequest {
	if v == nil {
		return nil
	}
	out := &challenge.ClaimWorkRequest{}
	out.WorkerInstanceId = v.WorkerInstanceId
	out.ClaimRequestId = v.ClaimRequestId
	for _, x := range v.Capabilities {
		out.Capabilities = append(out.Capabilities, challenge.WorkKind(x))
	}
	return out
}
func FromClaimWorkResponse(v *challenge.ClaimWorkResponse) *dto.ClaimWorkResponse {
	if v == nil {
		return nil
	}
	out := &dto.ClaimWorkResponse{}
	out.Assignment = FromAssignment(v.Assignment)
	return out
}
func ToClaimWorkResponse(v *dto.ClaimWorkResponse) *challenge.ClaimWorkResponse {
	if v == nil {
		return nil
	}
	out := &challenge.ClaimWorkResponse{}
	out.Assignment = ToAssignment(v.Assignment)
	return out
}
func FromRenewLeaseRequest(v *challenge.RenewLeaseRequest) *dto.RenewLeaseRequest {
	if v == nil {
		return nil
	}
	out := &dto.RenewLeaseRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	return out
}
func ToRenewLeaseRequest(v *dto.RenewLeaseRequest) *challenge.RenewLeaseRequest {
	if v == nil {
		return nil
	}
	out := &challenge.RenewLeaseRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	return out
}
func FromRenewLeaseResponse(v *challenge.RenewLeaseResponse) *dto.RenewLeaseResponse {
	if v == nil {
		return nil
	}
	out := &dto.RenewLeaseResponse{}
	out.ExpiresAt = fromTime(v.ExpiresAt)
	out.StopRequested = v.StopRequested
	return out
}
func ToRenewLeaseResponse(v *dto.RenewLeaseResponse) *challenge.RenewLeaseResponse {
	if v == nil {
		return nil
	}
	out := &challenge.RenewLeaseResponse{}
	out.ExpiresAt = toTime(v.ExpiresAt)
	out.StopRequested = v.StopRequested
	return out
}
func FromReportProgressRequest(v *challenge.ReportProgressRequest) *dto.ReportProgressRequest {
	if v == nil {
		return nil
	}
	out := &dto.ReportProgressRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.Sequence = v.Sequence
	out.Phase = v.Phase
	return out
}
func ToReportProgressRequest(v *dto.ReportProgressRequest) *challenge.ReportProgressRequest {
	if v == nil {
		return nil
	}
	out := &challenge.ReportProgressRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.Sequence = v.Sequence
	out.Phase = v.Phase
	return out
}
func FromReportProgressResponse(v *challenge.ReportProgressResponse) *dto.ReportProgressResponse {
	if v == nil {
		return nil
	}
	out := &dto.ReportProgressResponse{}
	out.AckSequence = v.AckSequence
	return out
}
func ToReportProgressResponse(v *dto.ReportProgressResponse) *challenge.ReportProgressResponse {
	if v == nil {
		return nil
	}
	out := &challenge.ReportProgressResponse{}
	out.AckSequence = v.AckSequence
	return out
}
func FromReadMaterialRequest(v *challenge.ReadMaterialRequest) *dto.ReadMaterialRequest {
	if v == nil {
		return nil
	}
	out := &dto.ReadMaterialRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.MaterialId = v.MaterialId
	out.Offset = v.Offset
	return out
}
func ToReadMaterialRequest(v *dto.ReadMaterialRequest) *challenge.ReadMaterialRequest {
	if v == nil {
		return nil
	}
	out := &challenge.ReadMaterialRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.MaterialId = v.MaterialId
	out.Offset = v.Offset
	return out
}
func FromReadMaterialResponse(v *challenge.ReadMaterialResponse) *dto.ReadMaterialResponse {
	if v == nil {
		return nil
	}
	out := &dto.ReadMaterialResponse{}
	out.Chunk = v.Chunk
	out.Offset = v.Offset
	out.Digest = v.Digest
	return out
}
func ToReadMaterialResponse(v *dto.ReadMaterialResponse) *challenge.ReadMaterialResponse {
	if v == nil {
		return nil
	}
	out := &challenge.ReadMaterialResponse{}
	out.Chunk = v.Chunk
	out.Offset = v.Offset
	out.Digest = v.Digest
	return out
}
func FromUploadArtifactRequest(v *challenge.UploadArtifactRequest) *dto.UploadArtifactRequest {
	if v == nil {
		return nil
	}
	out := &dto.UploadArtifactRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.UploadId = v.UploadId
	out.Filename = v.Filename
	out.MediaType = v.MediaType
	out.Bytes = v.Bytes
	out.Sha256 = v.Sha256
	out.Chunk = v.Chunk
	return out
}
func ToUploadArtifactRequest(v *dto.UploadArtifactRequest) *challenge.UploadArtifactRequest {
	if v == nil {
		return nil
	}
	out := &challenge.UploadArtifactRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.UploadId = v.UploadId
	out.Filename = v.Filename
	out.MediaType = v.MediaType
	out.Bytes = v.Bytes
	out.Sha256 = v.Sha256
	out.Chunk = v.Chunk
	return out
}
func FromUploadArtifactResponse(v *challenge.UploadArtifactResponse) *dto.UploadArtifactResponse {
	if v == nil {
		return nil
	}
	out := &dto.UploadArtifactResponse{}
	out.Asset = FromAsset(v.Asset)
	return out
}
func ToUploadArtifactResponse(v *dto.UploadArtifactResponse) *challenge.UploadArtifactResponse {
	if v == nil {
		return nil
	}
	out := &challenge.UploadArtifactResponse{}
	out.Asset = ToAsset(v.Asset)
	return out
}
func FromPromptResult(v *challenge.PromptResult) *domain.PromptResult {
	if v == nil {
		return nil
	}
	out := &domain.PromptResult{}
	out.Prompt = v.Prompt
	return out
}
func ToPromptResult(v *domain.PromptResult) *challenge.PromptResult {
	if v == nil {
		return nil
	}
	out := &challenge.PromptResult{}
	out.Prompt = v.Prompt
	return out
}
func FromGeneratedWork(v *challenge.GeneratedWork) *domain.GeneratedWork {
	if v == nil {
		return nil
	}
	out := &domain.GeneratedWork{}
	out.Work = FromWork(v.Work)
	out.Report = v.Report
	return out
}
func ToGeneratedWork(v *domain.GeneratedWork) *challenge.GeneratedWork {
	if v == nil {
		return nil
	}
	out := &challenge.GeneratedWork{}
	out.Work = ToWork(v.Work)
	out.Report = v.Report
	return out
}
func FromCompleteWorkRequest(v *challenge.CompleteWorkRequest) *dto.CompleteWorkRequest {
	if v == nil {
		return nil
	}
	out := &dto.CompleteWorkRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.ResultDigest = v.ResultDigest
	if x, ok := v.Result.(*challenge.CompleteWorkRequest_Prompt); ok {
		out.Result = &dto.CompleteWorkRequest_Prompt{Prompt: FromPromptResult(x.Prompt)}
	}
	if x, ok := v.Result.(*challenge.CompleteWorkRequest_Rankings); ok {
		out.Result = &dto.CompleteWorkRequest_Rankings{Rankings: FromRankings(x.Rankings)}
	}
	if x, ok := v.Result.(*challenge.CompleteWorkRequest_Generated); ok {
		out.Result = &dto.CompleteWorkRequest_Generated{Generated: FromGeneratedWork(x.Generated)}
	}
	if x, ok := v.Result.(*challenge.CompleteWorkRequest_Judgment); ok {
		out.Result = &dto.CompleteWorkRequest_Judgment{Judgment: FromJudgment(x.Judgment)}
	}
	return out
}
func ToCompleteWorkRequest(v *dto.CompleteWorkRequest) *challenge.CompleteWorkRequest {
	if v == nil {
		return nil
	}
	out := &challenge.CompleteWorkRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.ResultDigest = v.ResultDigest
	if x, ok := v.Result.(*dto.CompleteWorkRequest_Prompt); ok {
		out.Result = &challenge.CompleteWorkRequest_Prompt{Prompt: ToPromptResult(x.Prompt)}
	}
	if x, ok := v.Result.(*dto.CompleteWorkRequest_Rankings); ok {
		out.Result = &challenge.CompleteWorkRequest_Rankings{Rankings: ToRankings(x.Rankings)}
	}
	if x, ok := v.Result.(*dto.CompleteWorkRequest_Generated); ok {
		out.Result = &challenge.CompleteWorkRequest_Generated{Generated: ToGeneratedWork(x.Generated)}
	}
	if x, ok := v.Result.(*dto.CompleteWorkRequest_Judgment); ok {
		out.Result = &challenge.CompleteWorkRequest_Judgment{Judgment: ToJudgment(x.Judgment)}
	}
	return out
}
func FromCompleteWorkResponse(v *challenge.CompleteWorkResponse) *dto.CompleteWorkResponse {
	if v == nil {
		return nil
	}
	out := &dto.CompleteWorkResponse{}
	out.Accepted = v.Accepted
	return out
}
func ToCompleteWorkResponse(v *dto.CompleteWorkResponse) *challenge.CompleteWorkResponse {
	if v == nil {
		return nil
	}
	out := &challenge.CompleteWorkResponse{}
	out.Accepted = v.Accepted
	return out
}
func FromFailWorkRequest(v *challenge.FailWorkRequest) *dto.FailWorkRequest {
	if v == nil {
		return nil
	}
	out := &dto.FailWorkRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.FailureCode = v.FailureCode
	out.OutcomeKnown = v.OutcomeKnown
	return out
}
func ToFailWorkRequest(v *dto.FailWorkRequest) *challenge.FailWorkRequest {
	if v == nil {
		return nil
	}
	out := &challenge.FailWorkRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.FailureCode = v.FailureCode
	out.OutcomeKnown = v.OutcomeKnown
	return out
}
func FromFailWorkResponse(v *challenge.FailWorkResponse) *dto.FailWorkResponse {
	if v == nil {
		return nil
	}
	out := &dto.FailWorkResponse{}
	out.Accepted = v.Accepted
	return out
}
func ToFailWorkResponse(v *dto.FailWorkResponse) *challenge.FailWorkResponse {
	if v == nil {
		return nil
	}
	out := &challenge.FailWorkResponse{}
	out.Accepted = v.Accepted
	return out
}
func FromActivateAttemptRequest(v *challenge.ActivateAttemptRequest) *dto.ActivateAttemptRequest {
	if v == nil {
		return nil
	}
	out := &dto.ActivateAttemptRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.ExecutionInstanceId = v.ExecutionInstanceId
	return out
}
func ToActivateAttemptRequest(v *dto.ActivateAttemptRequest) *challenge.ActivateAttemptRequest {
	if v == nil {
		return nil
	}
	out := &challenge.ActivateAttemptRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.ExecutionInstanceId = v.ExecutionInstanceId
	return out
}
func FromActivateAttemptResponse(v *challenge.ActivateAttemptResponse) *dto.ActivateAttemptResponse {
	if v == nil {
		return nil
	}
	out := &dto.ActivateAttemptResponse{}
	out.Grant = v.Grant
	out.ExpiresAt = fromTime(v.ExpiresAt)
	return out
}
func ToActivateAttemptResponse(v *dto.ActivateAttemptResponse) *challenge.ActivateAttemptResponse {
	if v == nil {
		return nil
	}
	out := &challenge.ActivateAttemptResponse{}
	out.Grant = v.Grant
	out.ExpiresAt = toTime(v.ExpiresAt)
	return out
}
func FromCheckTaskAccessRequest(v *challenge.CheckTaskAccessRequest) *dto.CheckTaskAccessRequest {
	if v == nil {
		return nil
	}
	out := &dto.CheckTaskAccessRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.Grant = v.Grant
	out.Operation = v.Operation
	out.ResourceRef = v.ResourceRef
	return out
}
func ToCheckTaskAccessRequest(v *dto.CheckTaskAccessRequest) *challenge.CheckTaskAccessRequest {
	if v == nil {
		return nil
	}
	out := &challenge.CheckTaskAccessRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.Grant = v.Grant
	out.Operation = v.Operation
	out.ResourceRef = v.ResourceRef
	return out
}
func FromCheckTaskAccessResponse(v *challenge.CheckTaskAccessResponse) *dto.CheckTaskAccessResponse {
	if v == nil {
		return nil
	}
	out := &dto.CheckTaskAccessResponse{}
	out.Allowed = v.Allowed
	out.ExpiresAt = fromTime(v.ExpiresAt)
	return out
}
func ToCheckTaskAccessResponse(v *dto.CheckTaskAccessResponse) *challenge.CheckTaskAccessResponse {
	if v == nil {
		return nil
	}
	out := &challenge.CheckTaskAccessResponse{}
	out.Allowed = v.Allowed
	out.ExpiresAt = toTime(v.ExpiresAt)
	return out
}
func FromReserveModelCallRequest(v *challenge.ReserveModelCallRequest) *dto.ReserveModelCallRequest {
	if v == nil {
		return nil
	}
	out := &dto.ReserveModelCallRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.Grant = v.Grant
	out.RequestId = v.RequestId
	out.RequestDigest = v.RequestDigest
	out.Model = v.Model
	return out
}
func ToReserveModelCallRequest(v *dto.ReserveModelCallRequest) *challenge.ReserveModelCallRequest {
	if v == nil {
		return nil
	}
	out := &challenge.ReserveModelCallRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.Grant = v.Grant
	out.RequestId = v.RequestId
	out.RequestDigest = v.RequestDigest
	out.Model = v.Model
	return out
}
func FromReserveModelCallResponse(v *challenge.ReserveModelCallResponse) *dto.ReserveModelCallResponse {
	if v == nil {
		return nil
	}
	out := &dto.ReserveModelCallResponse{}
	out.ReservationId = v.ReservationId
	out.State = v.State
	out.DispatchPermit = v.DispatchPermit
	return out
}
func ToReserveModelCallResponse(v *dto.ReserveModelCallResponse) *challenge.ReserveModelCallResponse {
	if v == nil {
		return nil
	}
	out := &challenge.ReserveModelCallResponse{}
	out.ReservationId = v.ReservationId
	out.State = v.State
	out.DispatchPermit = v.DispatchPermit
	return out
}
func FromSettleModelCallRequest(v *challenge.SettleModelCallRequest) *dto.SettleModelCallRequest {
	if v == nil {
		return nil
	}
	out := &dto.SettleModelCallRequest{}
	out.Attempt = FromAttemptRef(v.Attempt)
	out.ReservationId = v.ReservationId
	out.Outcome = v.Outcome
	return out
}
func ToSettleModelCallRequest(v *dto.SettleModelCallRequest) *challenge.SettleModelCallRequest {
	if v == nil {
		return nil
	}
	out := &challenge.SettleModelCallRequest{}
	out.Attempt = ToAttemptRef(v.Attempt)
	out.ReservationId = v.ReservationId
	out.Outcome = v.Outcome
	return out
}
func FromSettleModelCallResponse(v *challenge.SettleModelCallResponse) *dto.SettleModelCallResponse {
	if v == nil {
		return nil
	}
	out := &dto.SettleModelCallResponse{}
	out.State = v.State
	return out
}
func ToSettleModelCallResponse(v *dto.SettleModelCallResponse) *challenge.SettleModelCallResponse {
	if v == nil {
		return nil
	}
	out := &challenge.SettleModelCallResponse{}
	out.State = v.State
	return out
}
func FromState(v *challenge.StoredRun) *domain.State {
	if v == nil {
		return nil
	}
	out := &domain.State{}
	out.Run = FromRun(v.Run)
	out.Task = FromTaskDescription(v.Task)
	out.Target = FromSnapshotRef(v.Target)
	for _, x := range v.Works {
		out.Works = append(out.Works, FromWork(x))
	}
	for _, x := range v.Comments {
		out.Comments = append(out.Comments, FromComment(x))
	}
	out.RankerPrompt = v.RankerPrompt
	out.ExecutionHints = v.ExecutionHints
	out.Assignment = FromAssignment(v.Assignment)
	out.Pending = v.Pending
	out.TerminalTarget = v.TerminalTarget
	out.Epoch = v.Epoch
	out.GrantHash = v.GrantHash
	out.ExecutionInstanceId = v.ExecutionInstanceId
	out.Validation = FromPreferenceBatch(v.Validation)
	out.Training = FromPreferenceBatch(v.Training)
	for _, x := range v.TrainingInputs {
		out.TrainingInputs = append(out.TrainingInputs, FromRankingSample(x))
	}
	out.TrainingRankings = FromRankings(v.TrainingRankings)
	out.SeenTaskIds = append(out.SeenTaskIds, v.SeenTaskIds...)
	out.FitBinding = v.FitBinding
	out.Feedback = FromOwnFeedback(v.Feedback)
	out.SourceAttemptId = v.SourceAttemptId
	out.ReservedCalls = v.ReservedCalls
	out.ProgressSequence = v.ProgressSequence
	out.ProgressPhase = v.ProgressPhase
	return out
}
func ToState(v *domain.State) *challenge.StoredRun {
	if v == nil {
		return nil
	}
	out := &challenge.StoredRun{}
	out.Run = ToRun(v.Run)
	out.Task = ToTaskDescription(v.Task)
	out.Target = ToSnapshotRef(v.Target)
	for _, x := range v.Works {
		out.Works = append(out.Works, ToWork(x))
	}
	for _, x := range v.Comments {
		out.Comments = append(out.Comments, ToComment(x))
	}
	out.RankerPrompt = v.RankerPrompt
	out.ExecutionHints = v.ExecutionHints
	out.Assignment = ToAssignment(v.Assignment)
	out.Pending = v.Pending
	out.TerminalTarget = v.TerminalTarget
	out.Epoch = v.Epoch
	out.GrantHash = v.GrantHash
	out.ExecutionInstanceId = v.ExecutionInstanceId
	out.Validation = ToPreferenceBatch(v.Validation)
	out.Training = ToPreferenceBatch(v.Training)
	for _, x := range v.TrainingInputs {
		out.TrainingInputs = append(out.TrainingInputs, ToRankingSample(x))
	}
	out.TrainingRankings = ToRankings(v.TrainingRankings)
	out.SeenTaskIds = append(out.SeenTaskIds, v.SeenTaskIds...)
	out.FitBinding = v.FitBinding
	out.Feedback = ToOwnFeedback(v.Feedback)
	out.SourceAttemptId = v.SourceAttemptId
	out.ReservedCalls = v.ReservedCalls
	out.ProgressSequence = v.ProgressSequence
	out.ProgressPhase = v.ProgressPhase
	return out
}
func FromArtifact(v *content.Artifact) *domain.Artifact {
	if v == nil {
		return nil
	}
	out := &domain.Artifact{}
	if x, ok := v.Value.(*content.Artifact_Text); ok {
		out.Value = &domain.Artifact_Text{Text: x.Text}
	}
	if x, ok := v.Value.(*content.Artifact_File); ok {
		out.Value = &domain.Artifact_File{File: FromAsset(x.File)}
	}
	if x, ok := v.Value.(*content.Artifact_Link); ok {
		out.Value = &domain.Artifact_Link{Link: x.Link}
	}
	return out
}
func ToArtifact(v *domain.Artifact) *content.Artifact {
	if v == nil {
		return nil
	}
	out := &content.Artifact{}
	if x, ok := v.Value.(*domain.Artifact_Text); ok {
		out.Value = &content.Artifact_Text{Text: x.Text}
	}
	if x, ok := v.Value.(*domain.Artifact_File); ok {
		out.Value = &content.Artifact_File{File: ToAsset(x.File)}
	}
	if x, ok := v.Value.(*domain.Artifact_Link); ok {
		out.Value = &content.Artifact_Link{Link: x.Link}
	}
	return out
}
func FromWork(v *content.Work) *domain.Work {
	if v == nil {
		return nil
	}
	out := &domain.Work{}
	out.Id = v.Id
	for _, x := range v.Artifacts {
		out.Artifacts = append(out.Artifacts, FromArtifact(x))
	}
	return out
}
func ToWork(v *domain.Work) *content.Work {
	if v == nil {
		return nil
	}
	out := &content.Work{}
	out.Id = v.Id
	for _, x := range v.Artifacts {
		out.Artifacts = append(out.Artifacts, ToArtifact(x))
	}
	return out
}
func FromTaskAttachment(v *content.TaskAttachment) *domain.TaskAttachment {
	if v == nil {
		return nil
	}
	out := &domain.TaskAttachment{}
	out.Asset = FromAsset(v.Asset)
	out.CloudUseAllowed = v.CloudUseAllowed
	return out
}
func ToTaskAttachment(v *domain.TaskAttachment) *content.TaskAttachment {
	if v == nil {
		return nil
	}
	out := &content.TaskAttachment{}
	out.Asset = ToAsset(v.Asset)
	out.CloudUseAllowed = v.CloudUseAllowed
	return out
}
func FromTaskDescription(v *content.TaskDescription) *domain.TaskDescription {
	if v == nil {
		return nil
	}
	out := &domain.TaskDescription{}
	out.TaskId = v.TaskId
	out.Revision = v.Revision
	out.Description = v.Description
	for _, x := range v.Attachments {
		out.Attachments = append(out.Attachments, FromTaskAttachment(x))
	}
	return out
}
func ToTaskDescription(v *domain.TaskDescription) *content.TaskDescription {
	if v == nil {
		return nil
	}
	out := &content.TaskDescription{}
	out.TaskId = v.TaskId
	out.Revision = v.Revision
	out.Description = v.Description
	for _, x := range v.Attachments {
		out.Attachments = append(out.Attachments, ToTaskAttachment(x))
	}
	return out
}
func FromComment(v *content.Comment) *domain.Comment {
	if v == nil {
		return nil
	}
	out := &domain.Comment{}
	out.Id = v.Id
	out.Body = v.Body
	return out
}
func ToComment(v *domain.Comment) *content.Comment {
	if v == nil {
		return nil
	}
	out := &content.Comment{}
	out.Id = v.Id
	out.Body = v.Body
	return out
}
func FromSnapshotRef(v *content.SnapshotRef) *domain.SnapshotRef {
	if v == nil {
		return nil
	}
	out := &domain.SnapshotRef{}
	out.TaskId = v.TaskId
	out.TaskRevision = v.TaskRevision
	out.CatalogRevision = v.CatalogRevision
	out.CommentCutoff = fromTime(v.CommentCutoff)
	return out
}
func ToSnapshotRef(v *domain.SnapshotRef) *content.SnapshotRef {
	if v == nil {
		return nil
	}
	out := &content.SnapshotRef{}
	out.TaskId = v.TaskId
	out.TaskRevision = v.TaskRevision
	out.CatalogRevision = v.CatalogRevision
	out.CommentCutoff = toTime(v.CommentCutoff)
	return out
}
func FromGetChallengeTaskDescriptionRequest(v *content.GetChallengeTaskDescriptionRequest) *dto.GetChallengeTaskDescriptionRequest {
	if v == nil {
		return nil
	}
	out := &dto.GetChallengeTaskDescriptionRequest{}
	out.Snapshot = FromSnapshotRef(v.Snapshot)
	return out
}
func ToGetChallengeTaskDescriptionRequest(v *dto.GetChallengeTaskDescriptionRequest) *content.GetChallengeTaskDescriptionRequest {
	if v == nil {
		return nil
	}
	out := &content.GetChallengeTaskDescriptionRequest{}
	out.Snapshot = ToSnapshotRef(v.Snapshot)
	return out
}
func FromGetChallengeTaskDescriptionResponse(v *content.GetChallengeTaskDescriptionResponse) *dto.GetChallengeTaskDescriptionResponse {
	if v == nil {
		return nil
	}
	out := &dto.GetChallengeTaskDescriptionResponse{}
	out.Task = FromTaskDescription(v.Task)
	out.Visible = v.Visible
	return out
}
func ToGetChallengeTaskDescriptionResponse(v *dto.GetChallengeTaskDescriptionResponse) *content.GetChallengeTaskDescriptionResponse {
	if v == nil {
		return nil
	}
	out := &content.GetChallengeTaskDescriptionResponse{}
	out.Task = ToTaskDescription(v.Task)
	out.Visible = v.Visible
	return out
}
func FromListChallengeWorksRequest(v *content.ListChallengeWorksRequest) *dto.ListChallengeWorksRequest {
	if v == nil {
		return nil
	}
	out := &dto.ListChallengeWorksRequest{}
	out.Snapshot = FromSnapshotRef(v.Snapshot)
	return out
}
func ToListChallengeWorksRequest(v *dto.ListChallengeWorksRequest) *content.ListChallengeWorksRequest {
	if v == nil {
		return nil
	}
	out := &content.ListChallengeWorksRequest{}
	out.Snapshot = ToSnapshotRef(v.Snapshot)
	return out
}
func FromListChallengeWorksResponse(v *content.ListChallengeWorksResponse) *dto.ListChallengeWorksResponse {
	if v == nil {
		return nil
	}
	out := &dto.ListChallengeWorksResponse{}
	for _, x := range v.Works {
		out.Works = append(out.Works, FromWork(x))
	}
	out.CatalogRevision = v.CatalogRevision
	return out
}
func ToListChallengeWorksResponse(v *dto.ListChallengeWorksResponse) *content.ListChallengeWorksResponse {
	if v == nil {
		return nil
	}
	out := &content.ListChallengeWorksResponse{}
	for _, x := range v.Works {
		out.Works = append(out.Works, ToWork(x))
	}
	out.CatalogRevision = v.CatalogRevision
	return out
}
func FromListChallengeCommentsRequest(v *content.ListChallengeCommentsRequest) *dto.ListChallengeCommentsRequest {
	if v == nil {
		return nil
	}
	out := &dto.ListChallengeCommentsRequest{}
	out.Snapshot = FromSnapshotRef(v.Snapshot)
	return out
}
func ToListChallengeCommentsRequest(v *dto.ListChallengeCommentsRequest) *content.ListChallengeCommentsRequest {
	if v == nil {
		return nil
	}
	out := &content.ListChallengeCommentsRequest{}
	out.Snapshot = ToSnapshotRef(v.Snapshot)
	return out
}
func FromListChallengeCommentsResponse(v *content.ListChallengeCommentsResponse) *dto.ListChallengeCommentsResponse {
	if v == nil {
		return nil
	}
	out := &dto.ListChallengeCommentsResponse{}
	for _, x := range v.Comments {
		out.Comments = append(out.Comments, FromComment(x))
	}
	out.CutoffAt = fromTime(v.CutoffAt)
	return out
}
func ToListChallengeCommentsResponse(v *dto.ListChallengeCommentsResponse) *content.ListChallengeCommentsResponse {
	if v == nil {
		return nil
	}
	out := &content.ListChallengeCommentsResponse{}
	for _, x := range v.Comments {
		out.Comments = append(out.Comments, ToComment(x))
	}
	out.CutoffAt = toTime(v.CutoffAt)
	return out
}
func FromMaterialRef(v *content.MaterialRef) *domain.MaterialRef {
	if v == nil {
		return nil
	}
	out := &domain.MaterialRef{}
	out.AssetId = v.AssetId
	out.SourceKind = v.SourceKind
	out.SourceId = v.SourceId
	return out
}
func ToMaterialRef(v *domain.MaterialRef) *content.MaterialRef {
	if v == nil {
		return nil
	}
	out := &content.MaterialRef{}
	out.AssetId = v.AssetId
	out.SourceKind = v.SourceKind
	out.SourceId = v.SourceId
	return out
}
func FromCheckChallengeMaterialsRequest(v *content.CheckChallengeMaterialsRequest) *dto.CheckChallengeMaterialsRequest {
	if v == nil {
		return nil
	}
	out := &dto.CheckChallengeMaterialsRequest{}
	out.RunId = v.RunId
	out.Snapshot = FromSnapshotRef(v.Snapshot)
	out.Purpose = v.Purpose
	out.WorkIds = append(out.WorkIds, v.WorkIds...)
	for _, x := range v.Materials {
		out.Materials = append(out.Materials, FromMaterialRef(x))
	}
	return out
}
func ToCheckChallengeMaterialsRequest(v *dto.CheckChallengeMaterialsRequest) *content.CheckChallengeMaterialsRequest {
	if v == nil {
		return nil
	}
	out := &content.CheckChallengeMaterialsRequest{}
	out.RunId = v.RunId
	out.Snapshot = ToSnapshotRef(v.Snapshot)
	out.Purpose = v.Purpose
	out.WorkIds = append(out.WorkIds, v.WorkIds...)
	for _, x := range v.Materials {
		out.Materials = append(out.Materials, ToMaterialRef(x))
	}
	return out
}
func FromCheckChallengeMaterialsResponse(v *content.CheckChallengeMaterialsResponse) *dto.CheckChallengeMaterialsResponse {
	if v == nil {
		return nil
	}
	out := &dto.CheckChallengeMaterialsResponse{}
	out.Allowed = v.Allowed
	out.AuthorizationVersion = v.AuthorizationVersion
	return out
}
func ToCheckChallengeMaterialsResponse(v *dto.CheckChallengeMaterialsResponse) *content.CheckChallengeMaterialsResponse {
	if v == nil {
		return nil
	}
	out := &content.CheckChallengeMaterialsResponse{}
	out.Allowed = v.Allowed
	out.AuthorizationVersion = v.AuthorizationVersion
	return out
}
func FromOpenRunRegistrationRequest(v *content.OpenRunRegistrationRequest) *dto.OpenRunRegistrationRequest {
	if v == nil {
		return nil
	}
	out := &dto.OpenRunRegistrationRequest{}
	out.RunId = v.RunId
	out.TaskId = v.TaskId
	return out
}
func ToOpenRunRegistrationRequest(v *dto.OpenRunRegistrationRequest) *content.OpenRunRegistrationRequest {
	if v == nil {
		return nil
	}
	out := &content.OpenRunRegistrationRequest{}
	out.RunId = v.RunId
	out.TaskId = v.TaskId
	return out
}
func FromOpenRunRegistrationResponse(v *content.OpenRunRegistrationResponse) *dto.OpenRunRegistrationResponse {
	if v == nil {
		return nil
	}
	out := &dto.OpenRunRegistrationResponse{}
	out.State = v.State
	return out
}
func ToOpenRunRegistrationResponse(v *dto.OpenRunRegistrationResponse) *content.OpenRunRegistrationResponse {
	if v == nil {
		return nil
	}
	out := &content.OpenRunRegistrationResponse{}
	out.State = v.State
	return out
}
func FromCloseRunRegistrationRequest(v *content.CloseRunRegistrationRequest) *dto.CloseRunRegistrationRequest {
	if v == nil {
		return nil
	}
	out := &dto.CloseRunRegistrationRequest{}
	out.RunId = v.RunId
	out.TaskId = v.TaskId
	return out
}
func ToCloseRunRegistrationRequest(v *dto.CloseRunRegistrationRequest) *content.CloseRunRegistrationRequest {
	if v == nil {
		return nil
	}
	out := &content.CloseRunRegistrationRequest{}
	out.RunId = v.RunId
	out.TaskId = v.TaskId
	return out
}
func FromCloseRunRegistrationResponse(v *content.CloseRunRegistrationResponse) *dto.CloseRunRegistrationResponse {
	if v == nil {
		return nil
	}
	out := &dto.CloseRunRegistrationResponse{}
	out.State = v.State
	return out
}
func ToCloseRunRegistrationResponse(v *dto.CloseRunRegistrationResponse) *content.CloseRunRegistrationResponse {
	if v == nil {
		return nil
	}
	out := &content.CloseRunRegistrationResponse{}
	out.State = v.State
	return out
}
func FromRegistrationReceipt(v *content.RegistrationReceipt) *domain.RegistrationReceipt {
	if v == nil {
		return nil
	}
	out := &domain.RegistrationReceipt{}
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	out.EntryId = v.EntryId
	out.ReviewId = v.ReviewId
	out.ReviewState = v.ReviewState
	out.Created = v.Created
	return out
}
func ToRegistrationReceipt(v *domain.RegistrationReceipt) *content.RegistrationReceipt {
	if v == nil {
		return nil
	}
	out := &content.RegistrationReceipt{}
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	out.EntryId = v.EntryId
	out.ReviewId = v.ReviewId
	out.ReviewState = v.ReviewState
	out.Created = v.Created
	return out
}
func FromRegisterChallengeCandidateRequest(v *content.RegisterChallengeCandidateRequest) *dto.RegisterChallengeCandidateRequest {
	if v == nil {
		return nil
	}
	out := &dto.RegisterChallengeCandidateRequest{}
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	out.TaskId = v.TaskId
	out.SourceAttemptId = v.SourceAttemptId
	out.Work = FromWork(v.Work)
	out.ExecutorConfigHash = v.ExecutorConfigHash
	return out
}
func ToRegisterChallengeCandidateRequest(v *dto.RegisterChallengeCandidateRequest) *content.RegisterChallengeCandidateRequest {
	if v == nil {
		return nil
	}
	out := &content.RegisterChallengeCandidateRequest{}
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	out.TaskId = v.TaskId
	out.SourceAttemptId = v.SourceAttemptId
	out.Work = ToWork(v.Work)
	out.ExecutorConfigHash = v.ExecutorConfigHash
	return out
}
func FromRegisterChallengeCandidateResponse(v *content.RegisterChallengeCandidateResponse) *dto.RegisterChallengeCandidateResponse {
	if v == nil {
		return nil
	}
	out := &dto.RegisterChallengeCandidateResponse{}
	out.Receipt = FromRegistrationReceipt(v.Receipt)
	return out
}
func ToRegisterChallengeCandidateResponse(v *dto.RegisterChallengeCandidateResponse) *content.RegisterChallengeCandidateResponse {
	if v == nil {
		return nil
	}
	out := &content.RegisterChallengeCandidateResponse{}
	out.Receipt = ToRegistrationReceipt(v.Receipt)
	return out
}
func FromGetChallengeRegistrationRequest(v *content.GetChallengeRegistrationRequest) *dto.GetChallengeRegistrationRequest {
	if v == nil {
		return nil
	}
	out := &dto.GetChallengeRegistrationRequest{}
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	return out
}
func ToGetChallengeRegistrationRequest(v *dto.GetChallengeRegistrationRequest) *content.GetChallengeRegistrationRequest {
	if v == nil {
		return nil
	}
	out := &content.GetChallengeRegistrationRequest{}
	out.RunId = v.RunId
	out.CandidateId = v.CandidateId
	return out
}
func FromGetChallengeRegistrationResponse(v *content.GetChallengeRegistrationResponse) *dto.GetChallengeRegistrationResponse {
	if v == nil {
		return nil
	}
	out := &dto.GetChallengeRegistrationResponse{}
	out.Receipt = FromRegistrationReceipt(v.Receipt)
	return out
}
func ToGetChallengeRegistrationResponse(v *dto.GetChallengeRegistrationResponse) *content.GetChallengeRegistrationResponse {
	if v == nil {
		return nil
	}
	out := &content.GetChallengeRegistrationResponse{}
	out.Receipt = ToRegistrationReceipt(v.Receipt)
	return out
}
func FromAsset(v *asset.Asset) *domain.Asset {
	if v == nil {
		return nil
	}
	out := &domain.Asset{}
	out.Id = v.Id
	out.Filename = v.Filename
	out.MediaType = v.MediaType
	out.Bytes = v.Bytes
	out.Sha256 = v.Sha256
	out.RunId = v.RunId
	out.AttemptId = v.AttemptId
	return out
}
func ToAsset(v *domain.Asset) *asset.Asset {
	if v == nil {
		return nil
	}
	out := &asset.Asset{}
	out.Id = v.Id
	out.Filename = v.Filename
	out.MediaType = v.MediaType
	out.Bytes = v.Bytes
	out.Sha256 = v.Sha256
	out.RunId = v.RunId
	out.AttemptId = v.AttemptId
	return out
}
func FromAccess(v *asset.Access) *domain.Access {
	if v == nil {
		return nil
	}
	out := &domain.Access{}
	out.TaskId = v.TaskId
	out.RunId = v.RunId
	out.AttemptId = v.AttemptId
	out.Purpose = v.Purpose
	return out
}
func ToAccess(v *domain.Access) *asset.Access {
	if v == nil {
		return nil
	}
	out := &asset.Access{}
	out.TaskId = v.TaskId
	out.RunId = v.RunId
	out.AttemptId = v.AttemptId
	out.Purpose = v.Purpose
	return out
}
func FromGetAssetRequest(v *asset.GetAssetRequest) *dto.GetAssetRequest {
	if v == nil {
		return nil
	}
	out := &dto.GetAssetRequest{}
	out.AssetId = v.AssetId
	out.Access = FromAccess(v.Access)
	return out
}
func ToGetAssetRequest(v *dto.GetAssetRequest) *asset.GetAssetRequest {
	if v == nil {
		return nil
	}
	out := &asset.GetAssetRequest{}
	out.AssetId = v.AssetId
	out.Access = ToAccess(v.Access)
	return out
}
func FromGetAssetResponse(v *asset.GetAssetResponse) *dto.GetAssetResponse {
	if v == nil {
		return nil
	}
	out := &dto.GetAssetResponse{}
	out.Asset = FromAsset(v.Asset)
	return out
}
func ToGetAssetResponse(v *dto.GetAssetResponse) *asset.GetAssetResponse {
	if v == nil {
		return nil
	}
	out := &asset.GetAssetResponse{}
	out.Asset = ToAsset(v.Asset)
	return out
}
func FromReadAssetRequest(v *asset.ReadAssetRequest) *dto.ReadAssetRequest {
	if v == nil {
		return nil
	}
	out := &dto.ReadAssetRequest{}
	out.AssetId = v.AssetId
	out.Access = FromAccess(v.Access)
	out.Offset = v.Offset
	return out
}
func ToReadAssetRequest(v *dto.ReadAssetRequest) *asset.ReadAssetRequest {
	if v == nil {
		return nil
	}
	out := &asset.ReadAssetRequest{}
	out.AssetId = v.AssetId
	out.Access = ToAccess(v.Access)
	out.Offset = v.Offset
	return out
}
func FromReadAssetResponse(v *asset.ReadAssetResponse) *dto.ReadAssetResponse {
	if v == nil {
		return nil
	}
	out := &dto.ReadAssetResponse{}
	out.Chunk = v.Chunk
	out.Offset = v.Offset
	out.Sha256 = v.Sha256
	return out
}
func ToReadAssetResponse(v *dto.ReadAssetResponse) *asset.ReadAssetResponse {
	if v == nil {
		return nil
	}
	out := &asset.ReadAssetResponse{}
	out.Chunk = v.Chunk
	out.Offset = v.Offset
	out.Sha256 = v.Sha256
	return out
}
func FromUploadAssetRequest(v *asset.UploadAssetRequest) *dto.UploadAssetRequest {
	if v == nil {
		return nil
	}
	out := &dto.UploadAssetRequest{}
	out.Access = FromAccess(v.Access)
	out.UploadId = v.UploadId
	out.Filename = v.Filename
	out.MediaType = v.MediaType
	out.Bytes = v.Bytes
	out.Sha256 = v.Sha256
	out.Chunk = v.Chunk
	return out
}
func ToUploadAssetRequest(v *dto.UploadAssetRequest) *asset.UploadAssetRequest {
	if v == nil {
		return nil
	}
	out := &asset.UploadAssetRequest{}
	out.Access = ToAccess(v.Access)
	out.UploadId = v.UploadId
	out.Filename = v.Filename
	out.MediaType = v.MediaType
	out.Bytes = v.Bytes
	out.Sha256 = v.Sha256
	out.Chunk = v.Chunk
	return out
}
func FromUploadAssetResponse(v *asset.UploadAssetResponse) *dto.UploadAssetResponse {
	if v == nil {
		return nil
	}
	out := &dto.UploadAssetResponse{}
	out.Asset = FromAsset(v.Asset)
	return out
}
func ToUploadAssetResponse(v *dto.UploadAssetResponse) *asset.UploadAssetResponse {
	if v == nil {
		return nil
	}
	out := &asset.UploadAssetResponse{}
	out.Asset = ToAsset(v.Asset)
	return out
}
func FromHumanPreference(v *voting.HumanPreference) *domain.HumanPreference {
	if v == nil {
		return nil
	}
	out := &domain.HumanPreference{}
	out.Snapshot = FromSnapshotRef(v.Snapshot)
	out.WorkSetHash = v.WorkSetHash
	out.WindowStart = fromTime(v.WindowStart)
	out.WindowEnd = fromTime(v.WindowEnd)
	out.CountsByWork = v.CountsByWork
	out.ValidVoteCount = v.ValidVoteCount
	return out
}
func ToHumanPreference(v *domain.HumanPreference) *voting.HumanPreference {
	if v == nil {
		return nil
	}
	out := &voting.HumanPreference{}
	out.Snapshot = ToSnapshotRef(v.Snapshot)
	out.WorkSetHash = v.WorkSetHash
	out.WindowStart = toTime(v.WindowStart)
	out.WindowEnd = toTime(v.WindowEnd)
	out.CountsByWork = v.CountsByWork
	out.ValidVoteCount = v.ValidVoteCount
	return out
}
func FromGetHumanPreferenceSnapshotRequest(v *voting.GetHumanPreferenceSnapshotRequest) *dto.GetHumanPreferenceSnapshotRequest {
	if v == nil {
		return nil
	}
	out := &dto.GetHumanPreferenceSnapshotRequest{}
	out.RunId = v.RunId
	out.Purpose = v.Purpose
	out.ExcludedTaskIds = append(out.ExcludedTaskIds, v.ExcludedTaskIds...)
	out.BatchIndex = v.BatchIndex
	return out
}
func ToGetHumanPreferenceSnapshotRequest(v *dto.GetHumanPreferenceSnapshotRequest) *voting.GetHumanPreferenceSnapshotRequest {
	if v == nil {
		return nil
	}
	out := &voting.GetHumanPreferenceSnapshotRequest{}
	out.RunId = v.RunId
	out.Purpose = v.Purpose
	out.ExcludedTaskIds = append(out.ExcludedTaskIds, v.ExcludedTaskIds...)
	out.BatchIndex = v.BatchIndex
	return out
}
func FromPreferenceBatch(v *voting.GetHumanPreferenceSnapshotResponse) *domain.PreferenceBatch {
	if v == nil {
		return nil
	}
	out := &domain.PreferenceBatch{}
	out.SnapshotId = v.SnapshotId
	out.PolicyVersion = v.PolicyVersion
	out.ValidUntil = fromTime(v.ValidUntil)
	for _, x := range v.Samples {
		out.Samples = append(out.Samples, FromHumanPreference(x))
	}
	return out
}
func ToPreferenceBatch(v *domain.PreferenceBatch) *voting.GetHumanPreferenceSnapshotResponse {
	if v == nil {
		return nil
	}
	out := &voting.GetHumanPreferenceSnapshotResponse{}
	out.SnapshotId = v.SnapshotId
	out.PolicyVersion = v.PolicyVersion
	out.ValidUntil = toTime(v.ValidUntil)
	for _, x := range v.Samples {
		out.Samples = append(out.Samples, ToHumanPreference(x))
	}
	return out
}
func FromCheckHumanPreferenceSnapshotRequest(v *voting.CheckHumanPreferenceSnapshotRequest) *dto.CheckHumanPreferenceSnapshotRequest {
	if v == nil {
		return nil
	}
	out := &dto.CheckHumanPreferenceSnapshotRequest{}
	out.SnapshotId = v.SnapshotId
	out.PolicyVersion = v.PolicyVersion
	return out
}
func ToCheckHumanPreferenceSnapshotRequest(v *dto.CheckHumanPreferenceSnapshotRequest) *voting.CheckHumanPreferenceSnapshotRequest {
	if v == nil {
		return nil
	}
	out := &voting.CheckHumanPreferenceSnapshotRequest{}
	out.SnapshotId = v.SnapshotId
	out.PolicyVersion = v.PolicyVersion
	return out
}
func FromCheckHumanPreferenceSnapshotResponse(v *voting.CheckHumanPreferenceSnapshotResponse) *dto.CheckHumanPreferenceSnapshotResponse {
	if v == nil {
		return nil
	}
	out := &dto.CheckHumanPreferenceSnapshotResponse{}
	out.Valid = v.Valid
	return out
}
func ToCheckHumanPreferenceSnapshotResponse(v *dto.CheckHumanPreferenceSnapshotResponse) *voting.CheckHumanPreferenceSnapshotResponse {
	if v == nil {
		return nil
	}
	out := &voting.CheckHumanPreferenceSnapshotResponse{}
	out.Valid = v.Valid
	return out
}
