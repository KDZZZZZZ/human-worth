package domain

import (
	"time"
)

type InitialTaskPackage struct {
	TaskRevision      int64
	TaskAttachmentIds []string
	Description       string
	WorkRequirements  string
}

type ModelConfiguration struct {
	Model      string
	Prompt     string
	Parameters map[string]any
}

func (v *ModelConfiguration) GetModel() string {
	if v != nil {
		return v.Model
	}
	return ""
}
func (v *ModelConfiguration) GetPrompt() string {
	if v != nil {
		return v.Prompt
	}
	return ""
}

type ExecutorConfiguration struct {
	Model      string
	Prompt     string
	Harness    string
	Skills     []string
	Parameters map[string]any
}

func (v *ExecutorConfiguration) GetModel() string {
	if v != nil {
		return v.Model
	}
	return ""
}
func (v *ExecutorConfiguration) GetPrompt() string {
	if v != nil {
		return v.Prompt
	}
	return ""
}
func (v *ExecutorConfiguration) GetHarness() string {
	if v != nil {
		return v.Harness
	}
	return ""
}

type Budget struct {
	Amount int64
	Unit   string
}

type RunConfiguration struct {
	InitialTaskPackage       *InitialTaskPackage
	Packer                   *ModelConfiguration
	Executor                 *ExecutorConfiguration
	Ranker                   *ModelConfiguration
	RankerFitThreshold       float64
	RankerMinComparablePairs int32
	RankerRoundLimit         int32
	RoundLimit               int32
	Budget                   *Budget
}

type RankerFit struct {
	Status      string
	Score       *float64
	EvaluatedAt *time.Time
}

func (v *RankerFit) GetScore() float64 {
	if v != nil && v.Score != nil {
		return *v.Score
	}
	return 0
}

type Candidate struct {
	Id                string
	RegistrationState string
	EntryId           string
	ReviewId          string
}

func (v *Candidate) GetId() string {
	if v != nil {
		return v.Id
	}
	return ""
}

type Run struct {
	Id              string
	TaskId          string
	ParentRunId     string
	AdministratorId string
	Status          string
	Stage           string
	RankerRound     int32
	Round           int32
	RankerFit       *RankerFit
	Configuration   *RunConfiguration
	FailureReason   string
	Candidates      []*Candidate
	CreatedAt       *time.Time
	UpdatedAt       *time.Time
	EndedAt         *time.Time
}

func (v *Run) GetId() string {
	if v != nil {
		return v.Id
	}
	return ""
}
func (v *Run) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type AttemptRef struct {
	RunId            string
	WorkItemId       string
	AttemptId        string
	LeaseEpoch       int64
	WorkerInstanceId string
}

func (v *AttemptRef) GetAttemptId() string {
	if v != nil {
		return v.AttemptId
	}
	return ""
}

type Judgment struct {
	WorkId       string
	Criteria     string
	Rationale    string
	EvidenceRefs []string
}

type Ranking struct {
	TaskId         string
	OrderedWorkIds []string
	Judgments      []*Judgment
}

func (v *Ranking) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type Rankings struct {
	Items []*Ranking
}

type RankingSample struct {
	Task     *TaskDescription
	Works    []*Work
	Comments []*Comment
}

type RankInput struct {
	Samples []*RankingSample
}

func (v *RankInput) GetSamples() []*RankingSample {
	if v != nil {
		return v.Samples
	}
	return nil
}

type TrainingSample struct {
	Sample      *RankingSample
	Ranking     *Ranking
	HumanCounts map[string]int64
}

type RefineRankerInput struct {
	Samples     []*TrainingSample
	PreviousFit float64
	Target      *RankingSample
	Initial     *InitialTaskPackage
}

func (v *RefineRankerInput) GetSamples() []*TrainingSample {
	if v != nil {
		return v.Samples
	}
	return nil
}

type OwnFeedback struct {
	Work           *Work
	Report         string
	Rank           int32
	PopulationSize int32
	Judgment       *Judgment
	RankerFitScore float64
	EvaluationId   string
}

func (v *OwnFeedback) GetWork() *Work {
	if v != nil {
		return v.Work
	}
	return nil
}
func (v *OwnFeedback) GetRank() int32 {
	if v != nil {
		return v.Rank
	}
	return 0
}
func (v *OwnFeedback) GetJudgment() *Judgment {
	if v != nil {
		return v.Judgment
	}
	return nil
}

type PackInput struct {
	Initial        *InitialTaskPackage
	ExecutionHints string
	Feedback       *OwnFeedback
}

type ExecuteInput struct {
	Initial        *InitialTaskPackage
	ExecutionHints string
}

type ExplainInput struct {
	TaskDescription string
	OwnWork         *Work
}

type Material struct {
	Id         string
	Asset      *Asset
	TaskId     string
	SourceKind string
	SourceId   string
}

func (v *Material) GetId() string {
	if v != nil {
		return v.Id
	}
	return ""
}
func (v *Material) GetAsset() *Asset {
	if v != nil {
		return v.Asset
	}
	return nil
}
func (v *Material) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type Assignment struct {
	Attempt         *AttemptRef
	Kind            WorkKind
	Role            string
	Model           *ModelConfiguration
	Executor        *ExecutorConfiguration
	Materials       []*Material
	LeaseExpiresAt  *time.Time
	Deadline        *time.Time
	ModelCallLimit  int32
	ToolCallLimit   int32
	InputPolicyHash string
	WorkItemId      string
	Input           isAssignment_Input
}

type isAssignment_Input interface{ isAssignment_Input() }
type Assignment_Rank struct{ Rank *RankInput }

func (*Assignment_Rank) isAssignment_Input() {}

type Assignment_Refine struct{ Refine *RefineRankerInput }

func (*Assignment_Refine) isAssignment_Input() {}

type Assignment_Pack struct{ Pack *PackInput }

func (*Assignment_Pack) isAssignment_Input() {}

type Assignment_Execute struct{ Execute *ExecuteInput }

func (*Assignment_Execute) isAssignment_Input() {}

type Assignment_Explain struct{ Explain *ExplainInput }

func (*Assignment_Explain) isAssignment_Input() {}
func (v *Assignment) GetModel() *ModelConfiguration {
	if v != nil {
		return v.Model
	}
	return nil
}
func (v *Assignment) GetRank() *RankInput {
	if v != nil {
		if x, ok := v.Input.(*Assignment_Rank); ok {
			return x.Rank
		}
	}
	return nil
}
func (v *Assignment) GetRefine() *RefineRankerInput {
	if v != nil {
		if x, ok := v.Input.(*Assignment_Refine); ok {
			return x.Refine
		}
	}
	return nil
}
func (v *Assignment) GetPack() *PackInput {
	if v != nil {
		if x, ok := v.Input.(*Assignment_Pack); ok {
			return x.Pack
		}
	}
	return nil
}
func (v *Assignment) GetExecute() *ExecuteInput {
	if v != nil {
		if x, ok := v.Input.(*Assignment_Execute); ok {
			return x.Execute
		}
	}
	return nil
}
func (v *Assignment) GetExplain() *ExplainInput {
	if v != nil {
		if x, ok := v.Input.(*Assignment_Explain); ok {
			return x.Explain
		}
	}
	return nil
}

type PromptResult struct {
	Prompt string
}

func (v *PromptResult) GetPrompt() string {
	if v != nil {
		return v.Prompt
	}
	return ""
}

type GeneratedWork struct {
	Work   *Work
	Report string
}

func (v *GeneratedWork) GetWork() *Work {
	if v != nil {
		return v.Work
	}
	return nil
}

type State struct {
	Run                 *Run
	Task                *TaskDescription
	Target              *SnapshotRef
	Works               []*Work
	Comments            []*Comment
	RankerPrompt        string
	ExecutionHints      string
	Assignment          *Assignment
	Pending             string
	TerminalTarget      string
	Epoch               int64
	GrantHash           string
	ExecutionInstanceId string
	Validation          *PreferenceBatch
	Training            *PreferenceBatch
	TrainingInputs      []*RankingSample
	TrainingRankings    *Rankings
	SeenTaskIds         []string
	FitBinding          string
	Feedback            *OwnFeedback
	SourceAttemptId     string
	ReservedCalls       int64
	ProgressSequence    int64
	ProgressPhase       string
}

func (v *State) GetRun() *Run {
	if v != nil {
		return v.Run
	}
	return nil
}

type Artifact struct {
	Value isArtifact_Value
}

type isArtifact_Value interface{ isArtifact_Value() }
type Artifact_Text struct{ Text string }

func (*Artifact_Text) isArtifact_Value() {}

type Artifact_File struct{ File *Asset }

func (*Artifact_File) isArtifact_Value() {}

type Artifact_Link struct{ Link string }

func (*Artifact_Link) isArtifact_Value() {}
func (v *Artifact) GetText() string {
	if v != nil {
		if x, ok := v.Value.(*Artifact_Text); ok {
			return x.Text
		}
	}
	return ""
}
func (v *Artifact) GetFile() *Asset {
	if v != nil {
		if x, ok := v.Value.(*Artifact_File); ok {
			return x.File
		}
	}
	return nil
}

type Work struct {
	Id        string
	Artifacts []*Artifact
}

func (v *Work) GetId() string {
	if v != nil {
		return v.Id
	}
	return ""
}

type TaskAttachment struct {
	Asset           *Asset
	CloudUseAllowed bool
}

func (v *TaskAttachment) GetAsset() *Asset {
	if v != nil {
		return v.Asset
	}
	return nil
}

type TaskDescription struct {
	TaskId      string
	Revision    int64
	Description string
	Attachments []*TaskAttachment
}

func (v *TaskDescription) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type Comment struct {
	Id   string
	Body string
}

func (v *Comment) GetId() string {
	if v != nil {
		return v.Id
	}
	return ""
}

type SnapshotRef struct {
	TaskId          string
	TaskRevision    int64
	CatalogRevision int64
	CommentCutoff   *time.Time
}

func (v *SnapshotRef) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}

type MaterialRef struct {
	AssetId    string
	SourceKind string
	SourceId   string
}

type RegistrationReceipt struct {
	RunId       string
	CandidateId string
	EntryId     string
	ReviewId    string
	ReviewState string
	Created     bool
}

type Asset struct {
	Id        string
	Filename  string
	MediaType string
	Bytes     int64
	Sha256    string
	RunId     string
	AttemptId string
}

func (v *Asset) GetId() string {
	if v != nil {
		return v.Id
	}
	return ""
}
func (v *Asset) GetSha256() string {
	if v != nil {
		return v.Sha256
	}
	return ""
}
func (v *Asset) GetAttemptId() string {
	if v != nil {
		return v.AttemptId
	}
	return ""
}

type Access struct {
	TaskId    string
	RunId     string
	AttemptId string
	Purpose   string
}

func (v *Access) GetTaskId() string {
	if v != nil {
		return v.TaskId
	}
	return ""
}
func (v *Access) GetAttemptId() string {
	if v != nil {
		return v.AttemptId
	}
	return ""
}

type HumanPreference struct {
	Snapshot       *SnapshotRef
	WorkSetHash    string
	WindowStart    *time.Time
	WindowEnd      *time.Time
	CountsByWork   map[string]int64
	ValidVoteCount int64
}

type PreferenceBatch struct {
	SnapshotId    string
	PolicyVersion string
	ValidUntil    *time.Time
	Samples       []*HumanPreference
}

func (v *PreferenceBatch) GetSnapshotId() string {
	if v != nil {
		return v.SnapshotId
	}
	return ""
}
func (v *PreferenceBatch) GetPolicyVersion() string {
	if v != nil {
		return v.PolicyVersion
	}
	return ""
}
func (v *PreferenceBatch) GetSamples() []*HumanPreference {
	if v != nil {
		return v.Samples
	}
	return nil
}
