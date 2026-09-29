package challengeworker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// 只模拟 kubectl 控制平面，不执行模型代码；真实隔离与 CNI 仍需集群验收。
func TestExecutorJobLifecycle(t *testing.T) {
	for _, scenario := range []string{"success", "cancel", "loose_policy", "job_conflict", "lost_create"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			script := `#!/usr/bin/env python3
import json, pathlib, sys
root = pathlib.Path(__file__).parent
args = sys.argv[1:]
args = args[7:]
if args[:2] == ['get', 'runtimeclass']:
    print(json.dumps({'handler':'runsc'}))
elif args[:2] == ['get', 'networkpolicy']:
    print((root/'policy.json').read_text())
elif args[:2] == ['get', 'job']:
    print((root/'job.json').read_text())
elif args[0] == 'create':
    obj = json.load(sys.stdin)
    if obj['kind'] == 'Job' and (root/'conflict').exists():
        sys.exit(1)
    if obj['kind'] == 'Job':
        obj['metadata']['uid'] = 'fixture-job-uid'
    (root/(obj['kind'].lower()+'.json')).write_text(json.dumps(obj))
    if obj['kind'] == 'Job' and (root/'lost').exists():
        sys.exit(1)
elif args[:2] == ['delete', 'job']:
    (root/'deleted').write_text('yes')
else:
    sys.exit(1)
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			policy := IsolationPolicy()
			if scenario == "loose_policy" {
				policy["egress"] = []any{map[string]any{}}
			}
			data, _ := json.Marshal(map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": "challenge-execution-isolation"}, "spec": policy}}})
			os.WriteFile(filepath.Join(dir, "policy.json"), data, 0600)
			os.WriteFile(filepath.Join(dir, "ca.crt"), []byte("fixture-ca"), 0600)
			if scenario == "job_conflict" {
				os.WriteFile(filepath.Join(dir, "conflict"), []byte("yes"), 0600)
				os.WriteFile(filepath.Join(dir, "job.json"), []byte(`{"metadata":{"annotations":{"human-worth-template":"different"}}}`), 0600)
			}
			if scenario == "lost_create" {
				os.WriteFile(filepath.Join(dir, "lost"), []byte("yes"), 0600)
			}
			provider, _ := NewProvider(ProviderConfig{BaseURL: "https://provider.invalid", Model: "test-model", Protocol: "completion", APIKey: "master-key-must-stay-outside"}, nil)
			k := &KubernetesExecutor{Client: clientFor(t, &workerRPC{}), Instance: "worker_fixture", Provider: provider, Profile: ExecutorProfile{Kubeconfig: "fixture-config", Context: "fixture-context", Namespace: "human-worth-execution", RuntimeClass: "gvisor", Image: "fixture@sha256:" + strings.Repeat("a", 64), RelayURL: "https://worker.invalid:9443", CAFile: filepath.Join(dir, "ca.crt")}}
			a := &pb.Assignment{Attempt: &pb.AttemptRef{AttemptId: "attempt_test"}, Kind: pb.WorkKind_WORK_KIND_EXECUTE_PACKAGE, Executor: &pb.ExecutorConfiguration{Harness: "completion-shell", Model: "test-model"}, Deadline: timestamppb.New(time.Now().Add(10 * time.Second))}
			ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
			defer cancel()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if _, err := os.Stat(filepath.Join(dir, "secret.json")); err != nil {
							continue
						}
						if scenario == "cancel" {
							cancel()
							return
						}
						request := httptest.NewRequest("POST", "/attempts/attempt_test/activate", nil)
						request.Header.Set("Authorization", "Bearer short-grant")
						w := httptest.NewRecorder()
						k.ServeHTTP(w, request)
						if w.Code != 204 {
							t.Error("activation failed")
							cancel()
							return
						}
						g := &pb.GeneratedWork{Work: &content.Work{Id: "work_attempt_test", Artifacts: []*content.Artifact{{Value: &content.Artifact_Text{Text: "受控测试作品"}}}}, Report: "测试完成"}
						body, _ := protojson.Marshal(g)
						request = httptest.NewRequest("POST", "/attempts/attempt_test/result", strings.NewReader(string(body)))
						request.Header.Set("Authorization", "Bearer short-grant")
						w = httptest.NewRecorder()
						k.ServeHTTP(w, request)
						if w.Code != 204 {
							t.Error("result failed")
						}
						return
					}
				}
			}()
			result, err := k.Execute(ctx, a, "short-grant")
			cancel()
			<-finished
			if scenario == "success" || scenario == "lost_create" {
				if err != nil || result.GetWork().GetId() != "work_attempt_test" {
					t.Fatalf("execution result: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid or cancelled executor accepted")
			}
			job, readErr := os.ReadFile(filepath.Join(dir, "job.json"))
			if scenario == "job_conflict" {
				if _, err := os.Stat(filepath.Join(dir, "deleted")); err == nil {
					t.Fatal("conflicting Job was deleted")
				}
				return
			}
			if scenario == "loose_policy" {
				if readErr == nil {
					t.Fatal("job created with permissive network")
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err := os.Stat(filepath.Join(dir, "deleted")); err != nil {
				t.Fatal("job not cleaned up")
			}
			if strings.Contains(string(job), "master-key-must-stay-outside") {
				t.Fatal("master credential in Job")
			}
			var object map[string]any
			if json.Unmarshal(job, &object) != nil {
				t.Fatal("invalid Job")
			}
			spec := object["spec"].(map[string]any)
			pod := spec["template"].(map[string]any)["spec"].(map[string]any)
			if spec["backoffLimit"].(float64) != 0 || pod["automountServiceAccountToken"].(bool) || pod["runtimeClassName"] != "gvisor" {
				t.Fatal("unsafe Job execution policy")
			}
			for _, v := range pod["volumes"].([]any) {
				if v.(map[string]any)["hostPath"] != nil {
					t.Fatal("host filesystem mounted")
				}
			}
		})
	}
}

// testExecutionRelay 走真实 HTTPS 与记账 RPC，只替代集群和供应商，不执行模型产生的命令。
func testExecutionRelay(t *testing.T, upstream *Provider) (*pb.Assignment, *Provider, executionRelay, *workerRPC) {
	t.Helper()
	rpc := &workerRPC{}
	a := &pb.Assignment{
		Attempt: &pb.AttemptRef{AttemptId: "attempt_test"}, Role: "E", Kind: pb.WorkKind_WORK_KIND_EXECUTE_PACKAGE,
		Executor: &pb.ExecutorConfiguration{Model: upstream.config.Model, Harness: "completion-shell"},
		Deadline: timestamppb.New(time.Now().Add(time.Minute)), ModelCallLimit: 3, ToolCallLimit: 2,
		Input: &pb.Assignment_Execute{Execute: &pb.ExecuteInput{Initial: &pb.InitialTaskPackage{Description: "测试任务", WorkRequirements: "交付一件作品"}, ExecutionHints: "保留完整内容"}},
	}
	k := &KubernetesExecutor{Client: clientFor(t, rpc), Provider: upstream, sessions: map[string]*executorSession{"attempt_test": {assignment: a, grant: "short-grant"}}}
	server := httptest.NewTLSServer(k)
	t.Cleanup(server.Close)
	r := executionRelay{client: server.Client(), base: server.URL + "/attempts/attempt_test", grant: "short-grant"}
	response, err := r.call(t.Context(), "POST", "/activate", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	provider, err := NewProvider(ProviderConfig{BaseURL: r.base, Model: upstream.config.Model, Protocol: "completion", APIKey: r.grant}, r.client)
	if err != nil {
		t.Fatal(err)
	}
	return a, provider, r, rpc
}

func TestCompletionExecutorLoop(t *testing.T) {
	for _, scenario := range []string{"text", "file", "invalid_json", "invalid_tool", "extra_arguments", "tool_limit", "model_limit", "duplicate_call"} {
		t.Run(scenario, func(t *testing.T) {
			calls, commands := 0, 0
			value := "执行工具得到的作品"
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request completionRequest
				if r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil || request.Stream || len(request.Tools) != 1 {
					t.Error("invalid native Completion request")
				}
				calls++
				answer := map[string]any{}
				reason := "tool_calls"
				if calls == 1 || scenario == "duplicate_call" {
					name, args := "run_command", `{"command":"cat /work/input/probe.txt"}`
					if scenario == "invalid_tool" {
						name = "read_comments"
					}
					if scenario == "extra_arguments" {
						args = `{"command":"cat probe.txt","url":"https://outside.invalid"}`
					}
					answer["tool_calls"] = []any{map[string]any{"id": "call_1", "type": "function", "function": map[string]string{"name": name, "arguments": args}}}
				} else {
					last := request.Messages[len(request.Messages)-1]
					if last.Role != "tool" || last.ToolCallID != "call_1" || last.Content != value {
						t.Error("command observation not returned")
					}
					reason = "stop"
					artifact := map[string]string{"kind": "text", "text": value}
					if scenario == "file" {
						artifact = map[string]string{"kind": "file", "path": "/work/output/result.txt", "mediaType": "text/plain"}
					}
					data, _ := json.Marshal(map[string]any{"artifacts": []any{artifact}, "report": "执行完成"})
					answer["content"] = string(data)
					if scenario == "invalid_json" {
						answer["content"] = "```json\n{}\n```"
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": answer, "finish_reason": reason}}})
			}))
			defer upstream.Close()
			p, err := NewProvider(ProviderConfig{BaseURL: upstream.URL, Model: "test-model", Protocol: "completion", APIKey: "test-key"}, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			a, provider, relay, rpc := testExecutionRelay(t, p)
			if scenario == "tool_limit" {
				a.ToolCallLimit = 0
			}
			if scenario == "model_limit" {
				a.ModelCallLimit = 1
			}
			result, err := completeExecution(t.Context(), a, provider, "probe.txt", func(_ context.Context, command string) (string, error) {
				commands++
				if command != "cat /work/input/probe.txt" {
					t.Fatal("wrong command dispatched")
				}
				return value, nil
			})
			if scenario != "text" && scenario != "file" && scenario != "invalid_json" {
				if err == nil {
					t.Fatal("invalid execution accepted")
				}
				if (scenario == "invalid_tool" || scenario == "extra_arguments" || scenario == "tool_limit") && commands != 0 {
					t.Fatal("invalid command executed")
				}
				if (scenario == "model_limit" || scenario == "duplicate_call") && commands != 1 {
					t.Fatal("command repeated after limit or duplicate call")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if scenario == "file" {
				if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			generated, err := collectExecutionOutput(t.Context(), relay, a.Attempt.AttemptId, root, root, result)
			if scenario == "invalid_json" {
				if err == nil {
					t.Fatal("non-JSON result accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if commands != 1 || calls != 2 || len(generated.Work.Artifacts) != 1 || len(rpc.outcomes) != 2 {
				t.Fatal("incomplete E tool/output/accounting cycle")
			}
			for _, outcome := range rpc.outcomes {
				if outcome != "succeeded" {
					t.Fatal("unsettled Completion call")
				}
			}
			if scenario == "text" && generated.Work.Artifacts[0].GetText() != value {
				t.Fatal("text artifact changed")
			}
			if scenario == "file" && generated.Work.Artifacts[0].GetFile().GetSha256() != digest([]byte(value)) {
				t.Fatal("file artifact changed")
			}
			data, _ := protojson.Marshal(generated)
			response, err := relay.call(t.Context(), "POST", "/result", bytes.NewReader(data), "")
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
		})
	}
}

func (s *workerRPC) UploadArtifact(stream grpc.ClientStreamingServer[pb.UploadArtifactRequest, pb.UploadArtifactResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	data := append([]byte(nil), first.Chunk...)
	for {
		frame, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		data = append(data, frame.Chunk...)
	}
	return stream.SendAndClose(&pb.UploadArtifactResponse{Asset: &asset.Asset{Id: "asset_result", AttemptId: first.Attempt.AttemptId, Filename: first.Filename, MediaType: first.MediaType, Bytes: int64(len(data)), Sha256: digest(data)}})
}

// 这里只执行固定测试命令；模型生成的命令始终交给隔离 E Pod。
func TestExecutionCommandLimits(t *testing.T) {
	// 不嵌入 bytes.Buffer，避免 io.Copy 通过其 ReadFrom 绕过自定义 Write 上限。
	bounded := &limitedBuffer{limit: 4}
	if _, err := io.Copy(bounded, io.LimitReader(strings.NewReader("abcdef"), 6)); err == nil || bounded.buffer.Len() > 4 {
		t.Fatal("cluster command output limit bypassed")
	}
	t.Setenv("CHALLENGE_PRIVATE_TEST_VALUE", "must-not-be-inherited")
	dir := t.TempDir()
	result, err := runCommand(t.Context(), dir, `printf '%s:%s' "$PWD" "$CHALLENGE_PRIVATE_TEST_VALUE"; exit 7`)
	if err != nil {
		t.Fatal(err)
	}
	var observation struct {
		ExitCode  int
		Output    string
		Truncated bool
	}
	if json.Unmarshal([]byte(result), &observation) != nil || observation.ExitCode != 7 || observation.Output != dir+":" {
		t.Fatal("command directory, exit code or environment not preserved")
	}
	result, err = runCommand(t.Context(), dir, "head -c 70000 /dev/zero")
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal([]byte(result), &observation) != nil || !observation.Truncated || len(observation.Output) != 64<<10 {
		t.Fatal("command output not bounded")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runCommand(ctx, dir, "sleep 5 & wait"); err == nil || time.Since(start) > 2*time.Second {
		t.Fatal("command group not cancelled promptly")
	}
}
