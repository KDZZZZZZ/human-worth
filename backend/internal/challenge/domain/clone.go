package domain

import (
	"maps"
	"slices"
)

func CloneInitialTaskPackage(v *InitialTaskPackage) *InitialTaskPackage {
	if v == nil {
		return nil
	}
	out := *v
	out.TaskAttachmentIds = slices.Clone(v.TaskAttachmentIds)
	return &out
}
func CloneModelConfiguration(v *ModelConfiguration) *ModelConfiguration {
	if v == nil {
		return nil
	}
	out := *v
	out.Parameters = maps.Clone(v.Parameters)
	return &out
}
func CloneExecutorConfiguration(v *ExecutorConfiguration) *ExecutorConfiguration {
	if v == nil {
		return nil
	}
	out := *v
	out.Skills = slices.Clone(v.Skills)
	out.Parameters = maps.Clone(v.Parameters)
	return &out
}
func CloneBudget(v *Budget) *Budget {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneRunConfiguration(v *RunConfiguration) *RunConfiguration {
	if v == nil {
		return nil
	}
	out := *v
	out.InitialTaskPackage = CloneInitialTaskPackage(v.InitialTaskPackage)
	out.Packer = CloneModelConfiguration(v.Packer)
	out.Executor = CloneExecutorConfiguration(v.Executor)
	out.Ranker = CloneModelConfiguration(v.Ranker)
	out.Budget = CloneBudget(v.Budget)
	return &out
}
func CloneRankerFit(v *RankerFit) *RankerFit {
	if v == nil {
		return nil
	}
	out := *v
	if v.Score != nil {
		x := *v.Score
		out.Score = &x
	}
	if v.EvaluatedAt != nil {
		x := *v.EvaluatedAt
		out.EvaluatedAt = &x
	}
	return &out
}
func CloneCandidate(v *Candidate) *Candidate {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneRun(v *Run) *Run {
	if v == nil {
		return nil
	}
	out := *v
	out.RankerFit = CloneRankerFit(v.RankerFit)
	out.Configuration = CloneRunConfiguration(v.Configuration)
	out.Candidates = slices.Clone(v.Candidates)
	for i, x := range v.Candidates {
		out.Candidates[i] = CloneCandidate(x)
	}
	if v.CreatedAt != nil {
		x := *v.CreatedAt
		out.CreatedAt = &x
	}
	if v.UpdatedAt != nil {
		x := *v.UpdatedAt
		out.UpdatedAt = &x
	}
	if v.EndedAt != nil {
		x := *v.EndedAt
		out.EndedAt = &x
	}
	return &out
}
func CloneAttemptRef(v *AttemptRef) *AttemptRef {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneJudgment(v *Judgment) *Judgment {
	if v == nil {
		return nil
	}
	out := *v
	out.EvidenceRefs = slices.Clone(v.EvidenceRefs)
	return &out
}
func CloneRanking(v *Ranking) *Ranking {
	if v == nil {
		return nil
	}
	out := *v
	out.OrderedWorkIds = slices.Clone(v.OrderedWorkIds)
	out.Judgments = slices.Clone(v.Judgments)
	for i, x := range v.Judgments {
		out.Judgments[i] = CloneJudgment(x)
	}
	return &out
}
func CloneRankings(v *Rankings) *Rankings {
	if v == nil {
		return nil
	}
	out := *v
	out.Items = slices.Clone(v.Items)
	for i, x := range v.Items {
		out.Items[i] = CloneRanking(x)
	}
	return &out
}
func CloneRankingSample(v *RankingSample) *RankingSample {
	if v == nil {
		return nil
	}
	out := *v
	out.Task = CloneTaskDescription(v.Task)
	out.Works = slices.Clone(v.Works)
	for i, x := range v.Works {
		out.Works[i] = CloneWork(x)
	}
	out.Comments = slices.Clone(v.Comments)
	for i, x := range v.Comments {
		out.Comments[i] = CloneComment(x)
	}
	return &out
}
func CloneRankInput(v *RankInput) *RankInput {
	if v == nil {
		return nil
	}
	out := *v
	out.Samples = slices.Clone(v.Samples)
	for i, x := range v.Samples {
		out.Samples[i] = CloneRankingSample(x)
	}
	return &out
}
func CloneTrainingSample(v *TrainingSample) *TrainingSample {
	if v == nil {
		return nil
	}
	out := *v
	out.Sample = CloneRankingSample(v.Sample)
	out.Ranking = CloneRanking(v.Ranking)
	out.HumanCounts = maps.Clone(v.HumanCounts)
	return &out
}
func CloneRefineRankerInput(v *RefineRankerInput) *RefineRankerInput {
	if v == nil {
		return nil
	}
	out := *v
	out.Target = CloneRankingSample(v.Target)
	out.Initial = CloneInitialTaskPackage(v.Initial)
	out.Samples = slices.Clone(v.Samples)
	for i, x := range v.Samples {
		out.Samples[i] = CloneTrainingSample(x)
	}
	return &out
}
func CloneOwnFeedback(v *OwnFeedback) *OwnFeedback {
	if v == nil {
		return nil
	}
	out := *v
	out.Work = CloneWork(v.Work)
	out.Judgment = CloneJudgment(v.Judgment)
	return &out
}
func ClonePackInput(v *PackInput) *PackInput {
	if v == nil {
		return nil
	}
	out := *v
	out.Initial = CloneInitialTaskPackage(v.Initial)
	out.Feedback = CloneOwnFeedback(v.Feedback)
	return &out
}
func CloneExecuteInput(v *ExecuteInput) *ExecuteInput {
	if v == nil {
		return nil
	}
	out := *v
	out.Initial = CloneInitialTaskPackage(v.Initial)
	return &out
}
func CloneExplainInput(v *ExplainInput) *ExplainInput {
	if v == nil {
		return nil
	}
	out := *v
	out.OwnWork = CloneWork(v.OwnWork)
	return &out
}
func CloneMaterial(v *Material) *Material {
	if v == nil {
		return nil
	}
	out := *v
	out.Asset = CloneAsset(v.Asset)
	return &out
}
func CloneAssignment(v *Assignment) *Assignment {
	if v == nil {
		return nil
	}
	out := *v
	out.Attempt = CloneAttemptRef(v.Attempt)
	out.Model = CloneModelConfiguration(v.Model)
	out.Executor = CloneExecutorConfiguration(v.Executor)
	out.Materials = slices.Clone(v.Materials)
	for i, x := range v.Materials {
		out.Materials[i] = CloneMaterial(x)
	}
	if v.LeaseExpiresAt != nil {
		x := *v.LeaseExpiresAt
		out.LeaseExpiresAt = &x
	}
	if v.Deadline != nil {
		x := *v.Deadline
		out.Deadline = &x
	}
	if x, ok := v.Input.(*Assignment_Rank); ok {
		out.Input = &Assignment_Rank{Rank: CloneRankInput(x.Rank)}
	}
	if x, ok := v.Input.(*Assignment_Refine); ok {
		out.Input = &Assignment_Refine{Refine: CloneRefineRankerInput(x.Refine)}
	}
	if x, ok := v.Input.(*Assignment_Pack); ok {
		out.Input = &Assignment_Pack{Pack: ClonePackInput(x.Pack)}
	}
	if x, ok := v.Input.(*Assignment_Execute); ok {
		out.Input = &Assignment_Execute{Execute: CloneExecuteInput(x.Execute)}
	}
	if x, ok := v.Input.(*Assignment_Explain); ok {
		out.Input = &Assignment_Explain{Explain: CloneExplainInput(x.Explain)}
	}
	return &out
}
func ClonePromptResult(v *PromptResult) *PromptResult {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneGeneratedWork(v *GeneratedWork) *GeneratedWork {
	if v == nil {
		return nil
	}
	out := *v
	out.Work = CloneWork(v.Work)
	return &out
}
func CloneState(v *State) *State {
	if v == nil {
		return nil
	}
	out := *v
	out.Run = CloneRun(v.Run)
	out.Task = CloneTaskDescription(v.Task)
	out.Target = CloneSnapshotRef(v.Target)
	out.Works = slices.Clone(v.Works)
	for i, x := range v.Works {
		out.Works[i] = CloneWork(x)
	}
	out.Comments = slices.Clone(v.Comments)
	for i, x := range v.Comments {
		out.Comments[i] = CloneComment(x)
	}
	out.Assignment = CloneAssignment(v.Assignment)
	out.Validation = ClonePreferenceBatch(v.Validation)
	out.Training = ClonePreferenceBatch(v.Training)
	out.TrainingInputs = slices.Clone(v.TrainingInputs)
	for i, x := range v.TrainingInputs {
		out.TrainingInputs[i] = CloneRankingSample(x)
	}
	out.TrainingRankings = CloneRankings(v.TrainingRankings)
	out.SeenTaskIds = slices.Clone(v.SeenTaskIds)
	out.Feedback = CloneOwnFeedback(v.Feedback)
	return &out
}
func CloneArtifact(v *Artifact) *Artifact {
	if v == nil {
		return nil
	}
	out := *v
	if x, ok := v.Value.(*Artifact_Text); ok {
		out.Value = &Artifact_Text{Text: x.Text}
	}
	if x, ok := v.Value.(*Artifact_File); ok {
		out.Value = &Artifact_File{File: CloneAsset(x.File)}
	}
	if x, ok := v.Value.(*Artifact_Link); ok {
		out.Value = &Artifact_Link{Link: x.Link}
	}
	return &out
}
func CloneWork(v *Work) *Work {
	if v == nil {
		return nil
	}
	out := *v
	out.Artifacts = slices.Clone(v.Artifacts)
	for i, x := range v.Artifacts {
		out.Artifacts[i] = CloneArtifact(x)
	}
	return &out
}
func CloneTaskAttachment(v *TaskAttachment) *TaskAttachment {
	if v == nil {
		return nil
	}
	out := *v
	out.Asset = CloneAsset(v.Asset)
	return &out
}
func CloneTaskDescription(v *TaskDescription) *TaskDescription {
	if v == nil {
		return nil
	}
	out := *v
	out.Attachments = slices.Clone(v.Attachments)
	for i, x := range v.Attachments {
		out.Attachments[i] = CloneTaskAttachment(x)
	}
	return &out
}
func CloneComment(v *Comment) *Comment {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneSnapshotRef(v *SnapshotRef) *SnapshotRef {
	if v == nil {
		return nil
	}
	out := *v
	if v.CommentCutoff != nil {
		x := *v.CommentCutoff
		out.CommentCutoff = &x
	}
	return &out
}
func CloneMaterialRef(v *MaterialRef) *MaterialRef {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneRegistrationReceipt(v *RegistrationReceipt) *RegistrationReceipt {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneAsset(v *Asset) *Asset {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneAccess(v *Access) *Access {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}
func CloneHumanPreference(v *HumanPreference) *HumanPreference {
	if v == nil {
		return nil
	}
	out := *v
	out.Snapshot = CloneSnapshotRef(v.Snapshot)
	if v.WindowStart != nil {
		x := *v.WindowStart
		out.WindowStart = &x
	}
	if v.WindowEnd != nil {
		x := *v.WindowEnd
		out.WindowEnd = &x
	}
	out.CountsByWork = maps.Clone(v.CountsByWork)
	return &out
}
func ClonePreferenceBatch(v *PreferenceBatch) *PreferenceBatch {
	if v == nil {
		return nil
	}
	out := *v
	if v.ValidUntil != nil {
		x := *v.ValidUntil
		out.ValidUntil = &x
	}
	out.Samples = slices.Clone(v.Samples)
	for i, x := range v.Samples {
		out.Samples[i] = CloneHumanPreference(x)
	}
	return &out
}
