package protobuf

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Existing protobuf bytes are the compatibility oracle, including presence,
// oneof variants, nanoseconds and integers that cannot survive a JSON float bridge.
func TestStateAndResultWireCompatibility(t *testing.T) {
	stamp := &timestamppb.Timestamp{Seconds: 1720000000, Nanos: 123456789}
	params, _ := structpb.NewStruct(map[string]any{"temperature": .25, "top_p": .8})
	zero := float64(0)
	file := &asset.Asset{Id: "asset_test", Filename: "work.txt", MediaType: "text/plain", Bytes: 1<<55 + 17, Sha256: "digest", RunId: "run_test", AttemptId: "attempt_test"}
	work := &content.Work{Id: "work_test", Artifacts: []*content.Artifact{{Value: &content.Artifact_Text{Text: "正文"}}, {Value: &content.Artifact_File{File: file}}, {Value: &content.Artifact_Link{Link: "https://example.test/"}}}}
	sample := &pb.RankingSample{Task: &content.TaskDescription{TaskId: "task_test", Revision: 1<<55 + 1, Description: "原始任务", Attachments: []*content.TaskAttachment{{Asset: file, CloudUseAllowed: true}}}, Works: []*content.Work{work}, Comments: []*content.Comment{{Id: "comment_test", Body: "私有评论"}}}
	preference := &voting.GetHumanPreferenceSnapshotResponse{SnapshotId: "snapshot_test", PolicyVersion: "v1", ValidUntil: stamp, Samples: []*voting.HumanPreference{{Snapshot: &content.SnapshotRef{TaskId: "task_test", TaskRevision: 1<<55 + 1, CatalogRevision: 1<<55 + 3, CommentCutoff: stamp}, WorkSetHash: "set_hash", WindowStart: stamp, WindowEnd: stamp, CountsByWork: map[string]int64{"work_test": 1<<55 + 7}, ValidVoteCount: 1<<55 + 7}}}
	initial := &pb.InitialTaskPackage{TaskRevision: 1<<55 + 1, TaskAttachmentIds: []string{"asset_test"}, Description: "固定说明", WorkRequirements: "作品要求"}
	feedback := &pb.OwnFeedback{Work: work, Report: "报告", Rank: 1, PopulationSize: 2, RankerFitScore: .9, EvaluationId: "evaluation_test", Judgment: &pb.Judgment{WorkId: work.Id, Criteria: "准确", Rationale: "根据正文", EvidenceRefs: []string{work.Id}}}
	assignments := []*pb.Assignment{
		{Input: &pb.Assignment_Rank{Rank: &pb.RankInput{Samples: []*pb.RankingSample{sample}}}},
		{Input: &pb.Assignment_Refine{Refine: &pb.RefineRankerInput{Samples: []*pb.TrainingSample{{Sample: sample, HumanCounts: map[string]int64{work.Id: 1<<55 + 7}}}, PreviousFit: .8}}},
		{Input: &pb.Assignment_Pack{Pack: &pb.PackInput{Initial: initial, ExecutionHints: "当前提示", Feedback: feedback}}},
		{Input: &pb.Assignment_Execute{Execute: &pb.ExecuteInput{Initial: initial, ExecutionHints: "当前提示"}}},
		{Input: &pb.Assignment_Explain{Explain: &pb.ExplainInput{TaskDescription: "任务", OwnWork: work}}},
	}
	for _, assignment := range assignments {
		assignment.Attempt = &pb.AttemptRef{RunId: "run_test", WorkItemId: "item_test", AttemptId: "attempt_test", LeaseEpoch: 1<<55 + 3, WorkerInstanceId: "worker_test"}
		assignment.Kind = pb.WorkKind_WORK_KIND_VALIDATE_RANKER
		assignment.Role = "R"
		assignment.WorkItemId = "item_test"
		assignment.Model = &pb.ModelConfiguration{Model: "model", Prompt: "通用规则", Parameters: params}
		assignment.Executor = &pb.ExecutorConfiguration{Model: "model", Prompt: "固定指令", Harness: "completion-shell", Skills: []string{"skill"}, Parameters: params}
		assignment.LeaseExpiresAt = stamp
		assignment.Deadline = stamp
		assignment.ModelCallLimit = 8
		assignment.ToolCallLimit = 16
		assignment.InputPolicyHash = "policy"
		assignment.Materials = []*pb.Material{{Id: "material_test", Asset: file, TaskId: "task_test", SourceKind: "task", SourceId: "task_test"}}
		before := &pb.StoredRun{Run: &pb.Run{Id: "run_test", TaskId: "task_test", AdministratorId: "account_test", ParentRunId: "parent_test", Status: "active", Stage: "optimizing_ranker", RankerRound: 1, Round: 2, RankerFit: &pb.RankerFit{Status: "pending", Score: &zero, EvaluatedAt: stamp}, Configuration: &pb.RunConfiguration{InitialTaskPackage: initial, Packer: assignment.Model, Ranker: assignment.Model, Executor: assignment.Executor, RankerFitThreshold: .8, RankerMinComparablePairs: 1, RankerRoundLimit: 2, RoundLimit: 2, Budget: &pb.Budget{Amount: 100, Unit: "model_calls"}}, CreatedAt: stamp, UpdatedAt: stamp}, Task: sample.Task, Target: preference.Samples[0].Snapshot, Works: sample.Works, Comments: sample.Comments, RankerPrompt: "当前 R 提示", ExecutionHints: "当前 P 提示", Assignment: assignment, Pending: "", TerminalTarget: "completed", Epoch: 1<<55 + 3, GrantHash: "grant_hash", ExecutionInstanceId: "execution_test", Validation: preference, Training: preference, TrainingInputs: []*pb.RankingSample{sample}, TrainingRankings: &pb.Rankings{Items: []*pb.Ranking{{TaskId: "task_test", OrderedWorkIds: []string{work.Id}, Judgments: []*pb.Judgment{feedback.Judgment}}}}, SeenTaskIds: []string{"task_test"}, FitBinding: "fit_hash", Feedback: feedback, SourceAttemptId: "attempt_test", ReservedCalls: 7, ProgressSequence: 1<<55 + 5, ProgressPhase: "evaluating"}
		value := FromState(before)
		after := ToState(domain.CloneState(value))
		oldBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(before)
		if err != nil {
			t.Fatal(err)
		}
		newBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(after)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(oldBytes, newBytes) {
			t.Fatalf("stored state changed for input %T", assignment.Input)
		}
		if (Fingerprints{}).Configuration(value.Run.Configuration) != messageHash(before.Run.Configuration) {
			t.Fatal("configuration idempotency fingerprint changed")
		}
	}
	for _, q := range []*pb.CompleteWorkRequest{
		{Result: &pb.CompleteWorkRequest_Prompt{Prompt: &pb.PromptResult{Prompt: "新提示"}}},
		{Result: &pb.CompleteWorkRequest_Generated{Generated: &pb.GeneratedWork{Work: work, Report: "报告"}}},
		{Result: &pb.CompleteWorkRequest_Rankings{Rankings: &pb.Rankings{Items: []*pb.Ranking{{TaskId: "task_test", OrderedWorkIds: []string{work.Id}, Judgments: []*pb.Judgment{feedback.Judgment}}}}}},
		{Result: &pb.CompleteWorkRequest_Judgment{Judgment: feedback.Judgment}},
	} {
		data, err := (proto.MarshalOptions{Deterministic: true}).Marshal(q)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		expected := hex.EncodeToString(sum[:])
		q.Attempt = &pb.AttemptRef{AttemptId: "attempt_test"}
		q.ResultDigest = expected
		if (Fingerprints{}).Result(FromCompleteWorkRequest(q)) != expected {
			t.Fatalf("result digest changed for %T", q.Result)
		}
	}
}
