//go:build live

package challengeworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"google.golang.org/protobuf/encoding/protojson"
	"os"
	"strings"
	"testing"
	"time"
)

// TestLiveProvider 仅在显式指定私有配置时请求真实模型，不包含业务材料或真人数据。
func TestLiveProvider(t *testing.T) {
	path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE")
	if path == "" {
		t.Skip("set private model configuration to opt in")
	}
	p, err := LoadProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	if err = p.Probe(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestLiveProviderTools 用随机测试文本验证供应商原生工具调用和 tool 消息回传，不评估排名质量。
func TestLiveProviderTools(t *testing.T) {
	path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE")
	if path == "" {
		t.Skip("set private model configuration to opt in")
	}
	p, err := LoadProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	request := completionRequest{Model: p.config.Model, MaxTokens: 128, Messages: []message{{Role: "user", Content: "Call read_material with materialRef probe_material exactly once. After receiving its result, return only the exact text it contains."}}, Tools: []any{map[string]any{"type": "function", "function": map[string]any{"name": "read_material", "description": "Read the selected test material.", "parameters": map[string]any{"type": "object", "properties": map[string]any{"materialRef": map[string]string{"type": "string"}}, "required": []string{"materialRef"}, "additionalProperties": false}}}}}
	body, _ := json.Marshal(request)
	first, _, err := p.complete(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	answer := first.Choices[0].Message
	if len(answer.ToolCalls) != 1 || answer.ToolCalls[0].Function.Name != "read_material" {
		t.Fatal("provider did not return the required native tool call")
	}
	call := answer.ToolCalls[0]
	var args map[string]string
	if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args["materialRef"] != "probe_material" {
		t.Fatal("invalid provider tool arguments")
	}
	value := "challenge-tool-" + randomID()
	request.Messages = append(request.Messages, message{Role: "assistant", Content: answer.Content, ToolCalls: answer.ToolCalls}, message{Role: "tool", ToolCallID: call.ID, Content: value})
	request.Tools = nil
	body, _ = json.Marshal(request)
	last, _, err := p.complete(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(last.Choices[0].Message.Content) != value {
		t.Fatal("provider did not consume the tool result")
	}
}

// TestLiveExecutorCompletion 验证 E 经中继使用同一真实 Completion 配置完成工具往返和作品回传。
// 工具观察为固定测试文本；这个检查不在宿主机执行模型命令，也不替代真实沙箱验收。
func TestLiveExecutorCompletion(t *testing.T) {
	path := os.Getenv("CHALLENGE_MODEL_CONFIG_FILE")
	if path == "" {
		t.Skip("set private model configuration to opt in")
	}
	upstream, err := LoadProvider(path)
	if err != nil {
		t.Fatal(err)
	}
	a, provider, relay, rpc := testExecutionRelay(t, upstream)
	a.GetExecute().Initial.Description = "这是协议测试。只调用 run_command 一次，command 准确为 cat /work/input/probe.txt；把工具返回的完整文本作为唯一 text 作品，不加前后缀，不另建文件。然后按规定 JSON 格式交付。"
	a.GetExecute().Initial.WorkRequirements = "一件 text 类型作品，正文与工具结果完全相同。"
	value := "completion-executor-" + randomID()
	commands := 0
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	result, err := completeExecution(ctx, a, provider, "/work/input/probe.txt", func(_ context.Context, command string) (string, error) {
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
