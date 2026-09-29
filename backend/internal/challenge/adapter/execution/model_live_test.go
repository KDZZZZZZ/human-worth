//go:build live

package execution

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/completion"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	challengerpc "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/rpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestLiveProvider 仅在显式指定私有配置时请求真实模型，不包含业务材料或真人数据。
func TestLiveProvider(t *testing.T) {
	path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE")
	if path == "" {
		t.Skip("set private model configuration to opt in")
	}
	p, err := completion.LoadProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err = p.Probe(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestLiveStructuredResult 用合成输入验证 P/R 的单次结构化结果，不调用材料工具。
func TestLiveStructuredResult(t *testing.T) {
	path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE")
	if path == "" {
		t.Skip("set private model configuration to opt in")
	}
	provider, err := completion.LoadProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	rpc := &workerRPC{}
	worker := &agent.Worker{Client: challengerpc.WorkerClient{Client: clientFor(t, rpc)}, Provider: provider, Codec: protobuf.AgentPayloads{}}
	a := &domain.Assignment{Role: "R", Kind: domain.WorkKind_WORK_KIND_REFINE_RANKER, Attempt: &domain.AttemptRef{AttemptId: "probe"}, Model: &domain.ModelConfiguration{Model: provider.Model(), Prompt: "为计算 1+1 的作品生成准确性判断标准。"}, ModelCallLimit: 1, InputPolicyHash: domain.InputPolicyHash(domain.InputPolicyVersion, 1, 0), Input: &domain.Assignment_Refine{Refine: &domain.RefineRankerInput{}}}
	result, err := worker.Generate(ctx, a, "probe-grant")
	if err != nil || strings.TrimSpace(result.GetPrompt().GetPrompt()) == "" {
		t.Fatalf("structured result required: %v", err)
	}
}

// TestLiveExecutorCompletion 验证 E 经中继使用同一真实 Completion 配置完成工具往返和作品回传。
// 工具观察为固定测试文本；这个检查不在宿主机执行模型命令，也不替代真实沙箱验收。
func TestLiveExecutorCompletion(t *testing.T) {
	path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE")
	if path == "" {
		t.Skip("set private model configuration to opt in")
	}
	upstream, err := completion.LoadProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	a, provider, relay, rpc := testExecutionRelay(t, upstream)
	a.GetExecute().Initial.Description = "这是协议测试。只调用 run_command 一次，command 准确为 cat /work/input/probe.txt；把工具返回的完整文本作为唯一 text 作品，不加前后缀，不另建文件。然后按规定 JSON 格式交付。"
	a.GetExecute().Initial.WorkRequirements = "一件 text 类型作品，正文与工具结果完全相同。"
	value := "completion-executor-" + domain.NewID("")
	commands := 0
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	result, err := agent.CompleteExecution(ctx, protobuf.FromAssignment(a), provider, protobuf.AgentPayloads{}, "/work/input/probe.txt", func(_ context.Context, command string) (string, error) {
		commands++
		if strings.TrimSpace(command) != "cat /work/input/probe.txt" {
			return "", errors.New("unexpected protocol test command")
		}
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	generated, err := collectExecutionOutput(ctx, relay, a.Attempt.AttemptId, root, root, result)
	if err != nil {
		t.Fatal(err)
	}
	if commands != 1 || len(generated.Work.Artifacts) != 1 || generated.Work.Artifacts[0].GetText() != value {
		t.Fatal("executor did not deliver exact tool observation")
	}
	data, _ := protojson.Marshal(generated)
	response, err := relay.call(ctx, "POST", "/result", bytes.NewReader(data), "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	rpc.mu.Lock()
	defer rpc.mu.Unlock()
	if len(rpc.outcomes) != 2 || rpc.outcomes[0] != "succeeded" || rpc.outcomes[1] != "succeeded" {
		t.Fatal("executor Completion calls not settled")
	}
}
