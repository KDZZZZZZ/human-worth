//go:build integration

package challenge_test

import (
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/rpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func (l *lab) storedRun(id string) *pb.StoredRun {
	l.t.Helper()
	var data []byte
	must(l.t, l.db.QueryRow(l.t.Context(), "SELECT state FROM challenge.runs WHERE id=$1", id).Scan(&data))
	state := &pb.StoredRun{}
	must(l.t, proto.Unmarshal(data, state))
	return state
}

// 真实 HTTP 模型调用与双副本 gRPC 验证标准先生成、再独立判断，后续验证不复用已暴露样本。
func TestRankerCriteriaBeforeEveryJudgment(t *testing.T) {
	l := newLab(t)
	cfg := config()
	cfg.Ranker.Prompt = "seed_not_a_judgment"
	run := l.start(cfg)
	worker := &agent.Worker{Codec: protobuf.AgentPayloads{}, Client: rpc.WorkerClient{Client: l.workers[0]}, Provider: l.modelProvider("baseline_reverse", "current_criteria_correctness"), Instance: "worker_0"}
	kinds := []pb.WorkKind{pb.WorkKind_WORK_KIND_REFINE_RANKER, pb.WorkKind_WORK_KIND_VALIDATE_RANKER, pb.WorkKind_WORK_KIND_REFINE_RANKER, pb.WorkKind_WORK_KIND_VALIDATE_RANKER}
	seen := map[string]bool{"target": true}
	for step, kind := range kinds {
		must(t, l.services[step%2].Reconcile(t.Context()))
		before := l.storedRun(run.Id)
		a := before.Assignment
		if a == nil || a.Kind != kind || a.ModelCallLimit != 1 || a.ToolCallLimit != 0 || before.Run.Stage != "optimizing_ranker" {
			t.Fatalf("step %d did not have a single-call criteria/judgment assignment: %v", step, a)
		}
		if kind == pb.WorkKind_WORK_KIND_REFINE_RANKER {
			input := a.GetRefine()
			if input.Target.GetTask().GetTaskId() != "target" || len(input.Target.Works) != 2 || len(input.Target.Comments) == 0 || !proto.Equal(input.Initial, cfg.InitialTaskPackage) || len(input.Samples[0].HumanCounts) != 2 {
				t.Fatal("criteria generation did not receive full authorized business context")
			}
			if step == 2 && (input.Samples[0].Ranking == nil || input.Samples[0].Sample.Task.TaskId != "validation_1" || input.PreviousFit != 0) {
				t.Fatal("next criteria generation lost actual judgments, labels or fit feedback")
			}
			for _, sample := range input.Samples {
				seen[sample.Sample.Task.TaskId] = true
			}
		} else {
			for _, sample := range a.GetRank().Samples {
				if seen[sample.Task.TaskId] {
					t.Fatal("independent judgment reused a task exposed to criteria generation")
				}
				seen[sample.Task.TaskId] = true
			}
		}
		worked, err := worker.RunOnce(t.Context())
		must(t, err)
		if !worked || l.deps.executions != 0 {
			t.Fatal("calibration did not execute or started E early")
		}
		after := l.storedRun(run.Id)
		if kind == pb.WorkKind_WORK_KIND_REFINE_RANKER && (after.RankerPrompt != []string{"baseline_reverse", "current_criteria_correctness"}[step/2] || after.Pending != "validation" || after.Assignment != nil) {
			t.Fatal("criteria were not durably saved before the separate judgment")
		}
		l.deps.mu.Lock()
		request := l.deps.requests[len(l.deps.requests)-1]
		calls := l.deps.modelCalls
		l.deps.mu.Unlock()
		var completion agent.CompletionRequest
		must(t, json.Unmarshal([]byte(request), &completion))
		if calls != step+1 || len(completion.Tools) != 0 || len(completion.Messages) != 2 || completion.Messages[0].Role != "system" || completion.Messages[1].Role != "user" {
			t.Fatal("P/R used tools, shared history or multiple model calls")
		}
		raw := strings.Split(completion.Messages[1].Content.(string), "\n文件清单：")[0]
		if kind == pb.WorkKind_WORK_KIND_REFINE_RANKER {
			input := &pb.RefineRankerInput{}
			must(t, protojson.Unmarshal([]byte(raw), input))
			if !strings.Contains(raw, privateComment) || input.Initial == nil || input.Target == nil || len(input.Samples[0].HumanCounts) == 0 {
				t.Fatal("authorized criteria material lost at the model boundary")
			}
		} else {
			if strings.Contains(raw, "humanCounts") || strings.Contains(raw, "管理员固定执行说明") || strings.Contains(completion.Messages[0].Content.(string), cfg.Ranker.Prompt) || !strings.Contains(completion.Messages[0].Content.(string), before.RankerPrompt) {
				t.Fatal("judgment did not use only the current generated criteria and its authorized inputs")
			}
		}
	}
	final := l.storedRun(run.Id)
	if final.Run.RankerFit.GetScore() != 1 || final.Run.RankerRound != 2 || final.Assignment.Kind != pb.WorkKind_WORK_KIND_PACK_TASK || final.RankerPrompt != "current_criteria_correctness" {
		t.Fatal("calibration did not freeze the successful criteria and unlock P")
	}
	var calls int
	must(t, l.db.QueryRow(t.Context(), "SELECT count(*) FROM challenge.model_calls WHERE run_id=$1 AND state='succeeded'", run.Id).Scan(&calls))
	if calls != 4 {
		t.Fatal("criteria and judgments were not individually accounted")
	}
}

func TestRankerCriteriaRoundLimit(t *testing.T) {
	l := newLab(t)
	cfg := config()
	cfg.RankerRoundLimit = 1
	run := l.start(cfg)
	worker := &agent.Worker{Codec: protobuf.AgentPayloads{}, Client: rpc.WorkerClient{Client: l.workers[0]}, Provider: l.modelProvider("baseline_reverse"), Instance: "worker_0"}
	for step := 0; step < 4; step++ {
		must(t, l.services[step%2].Reconcile(t.Context()))
		_, err := worker.RunOnce(t.Context())
		must(t, err)
	}
	final := l.get(run.Id)
	if final.Status != "failed" || final.FailureReason != "ranker_fit_not_met" || final.RankerRound != 1 || final.Round != 0 || l.deps.modelCalls != 2 || l.deps.executions != 0 {
		t.Fatal("round limit did not bound a complete criteria/judgment cycle")
	}
}
