package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/completion"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	challengerpc "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/rpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// workerRPC 用真实流式 gRPC 验证工具及中继；数据库并发另由 Challenge 集成测试覆盖。
type workerRPC struct {
	pb.UnimplementedChallengeServiceServer
	mu           sync.Mutex
	data         []byte
	digest       string
	reservations map[string]bool
	outcomes     []string
}

func (s *workerRPC) CheckTaskAccess(context.Context, *pb.CheckTaskAccessRequest) (*pb.CheckTaskAccessResponse, error) {
	return &pb.CheckTaskAccessResponse{Allowed: true}, nil
}
func (s *workerRPC) ReserveModelCall(_ context.Context, q *pb.ReserveModelCallRequest) (*pb.ReserveModelCallResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	permit := !s.reservations[q.RequestId]
	s.reservations[q.RequestId] = true
	return &pb.ReserveModelCallResponse{ReservationId: q.RequestId, DispatchPermit: permit}, nil
}
func (s *workerRPC) SettleModelCall(_ context.Context, q *pb.SettleModelCallRequest) (*pb.SettleModelCallResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outcomes = append(s.outcomes, q.Outcome)
	return &pb.SettleModelCallResponse{State: q.Outcome}, nil
}
func (s *workerRPC) ReadMaterial(q *pb.ReadMaterialRequest, stream grpc.ServerStreamingServer[pb.ReadMaterialResponse]) error {
	for offset := q.Offset; offset < int64(len(s.data)); {
		end := min(offset+65536, int64(len(s.data)))
		if err := stream.Send(&pb.ReadMaterialResponse{Offset: offset, Chunk: s.data[offset:end], Digest: s.digest}); err != nil {
			return err
		}
		offset = end
	}
	return nil
}
func clientFor(t *testing.T, s *workerRPC) pb.ChallengeServiceClient {
	t.Helper()
	s.reservations = map[string]bool{}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	pb.RegisterChallengeServiceServer(server, s)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return pb.NewChallengeServiceClient(conn)
}

func TestStructuredModelCall(t *testing.T) {
	for _, role := range []string{"P", "R"} {
		for _, scenario := range []string{"complete", "corrupt_digest", "truncated_material", "aggregate_limit", "tool_call", "invalid_json", "extra_field", "unknown_response"} {
			t.Run(role+"/"+scenario, func(t *testing.T) {
				data := []byte(strings.Repeat("中文材料", 9000))
				rpc := &workerRPC{data: data, digest: digest(data)}
				if scenario == "corrupt_digest" {
					rpc.digest = strings.Repeat("0", 64)
				}
				if scenario == "truncated_material" {
					rpc.data = data[:len(data)-1]
				}
				client := clientFor(t, rpc)
				calls := 0
				model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					var request agent.CompletionRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil {
						t.Error("bad model request")
						return
					}
					if len(request.Tools) != 0 || len(request.Messages) != 3 {
						t.Error("P/R must receive a fresh context without tools")
					}
					for _, message := range request.Messages {
						if message.Role == "tool" || message.Role == "assistant" {
							t.Error("P/R received conversation history")
						}
					}
					if request.Messages[len(request.Messages)-1].Content != "文件 material：\n"+string(data) {
						t.Error("model did not receive exact complete UTF-8 material")
					}
					if scenario == "unknown_response" {
						io.WriteString(w, `{"choices":[`)
						return
					}
					message := map[string]any{"role": "assistant", "content": `{"prompt":"根据任务要求和准确性判断。"}`}
					reason := "stop"
					switch scenario {
					case "tool_call":
						reason = "tool_calls"
						message["tool_calls"] = []any{map[string]any{"id": "call", "type": "function", "function": map[string]string{"name": "read_material", "arguments": `{}`}}}
					case "invalid_json":
						message["content"] = "plain text is not a structured result"
					case "extra_field":
						message["content"] = `{"prompt":"标准","unapproved":true}`
					}
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": reason}}})
				}))
				defer model.Close()
				provider, err := completion.NewProvider(completion.ProviderConfig{BaseURL: model.URL, Protocol: "completion", Model: "test-model", APIKey: "fixture"}, model.Client())
				if err != nil {
					t.Fatal(err)
				}
				a := &pb.Assignment{Role: role, Kind: pb.WorkKind_WORK_KIND_REFINE_RANKER, Attempt: &pb.AttemptRef{AttemptId: "attempt_test"}, Model: &pb.ModelConfiguration{Model: "test-model"}, ModelCallLimit: 1, Input: &pb.Assignment_Refine{Refine: &pb.RefineRankerInput{}}, Materials: []*pb.Material{{Id: "material", Asset: &asset.Asset{Id: "asset", Filename: "材料.txt", MediaType: "text/plain", Bytes: int64(len(data)), Sha256: rpc.digest}}}}
				if role == "P" {
					a.Kind = pb.WorkKind_WORK_KIND_PACK_TASK
					a.Input = &pb.Assignment_Pack{Pack: &pb.PackInput{}}
				}
				a.InputPolicyHash = domain.InputPolicyHash(domain.InputPolicyVersion, 1, 0)
				if scenario == "aggregate_limit" {
					a.Materials[0].Asset.Bytes = 16 << 20
					a.Materials = append(a.Materials, a.Materials[0])
				}
				worker := &agent.Worker{Client: challengerpc.WorkerClient{Client: client}, Provider: provider, Codec: protobuf.AgentPayloads{}}
				result, err := worker.Generate(t.Context(), protobuf.FromAssignment(a), "grant")
				if scenario == "complete" {
					if err != nil || result.GetPrompt().GetPrompt() != "根据任务要求和准确性判断。" {
						t.Fatalf("structured result: %v, %v", result, err)
					}
				} else if err == nil {
					t.Fatal("invalid material or model result accepted")
				}
				wantCalls := 1
				if scenario == "corrupt_digest" || scenario == "truncated_material" || scenario == "aggregate_limit" {
					wantCalls = 0
				}
				rpc.mu.Lock()
				defer rpc.mu.Unlock()
				if calls != wantCalls || len(rpc.reservations) != wantCalls || len(rpc.outcomes) != wantCalls {
					t.Fatalf("unexpected calls/settlements: %d %d %v", calls, len(rpc.reservations), rpc.outcomes)
				}
				if scenario == "unknown_response" && rpc.outcomes[0] != "unknown" {
					t.Fatal("unknown model result lost its reservation")
				}
			})
		}
	}
}

func TestCompletionRelayAccounting(t *testing.T) {
	for _, scenario := range []string{"complete", "truncated", "length", "hosted_tool", "wrong_grant", "wrong_model", "stream", "foreign_history", "remote_image", "old_endpoint"} {
		t.Run(scenario, func(t *testing.T) {
			rpc := &workerRPC{}
			client := clientFor(t, rpc)
			var upstreamCalls int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamCalls++
				var request agent.CompletionRequest
				if r.Header.Get("Authorization") != "Bearer upstream-secret" || r.URL.Path != "/v1/chat/completions" || json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("bad completion upstream request")
				}
				if request.Stream || request.MaxTokens != 8192 || request.Temperature == nil || *request.Temperature != 0.25 || request.TopP == nil || *request.TopP != 0.8 {
					t.Error("executor parameters not fixed by assignment")
				}
				w.Header().Set("Content-Type", "application/json")
				if scenario == "truncated" {
					io.WriteString(w, `{"choices":[{"message":{"content":"finish_reason: stop"}}`)
				} else {
					reason := "stop"
					if scenario == "length" {
						reason = "length"
					}
					json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": "OK"}, "finish_reason": reason}}})
				}
			}))
			defer upstream.Close()
			provider, err := completion.NewProvider(completion.ProviderConfig{BaseURL: upstream.URL, Protocol: "completion", Model: "test-model", APIKey: "upstream-secret"}, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			parameters, _ := structpb.NewStruct(map[string]any{"temperature": 0.25, "top_p": 0.8})
			a := &pb.Assignment{Attempt: &pb.AttemptRef{AttemptId: "attempt_test"}, Executor: &pb.ExecutorConfiguration{Model: "test-model", Parameters: parameters}, Deadline: timestamppb.New(time.Now().Add(time.Minute))}
			k := &KubernetesExecutor{Client: client, Provider: provider, sessions: map[string]*executorSession{"attempt_test": {assignment: a, grant: "short-grant", activated: true}}}
			body := map[string]any{"model": "test-model", "messages": []any{map[string]string{"role": "user", "content": "OK"}}, "tools": agent.ExecutionTools(), "temperature": 2, "top_p": 0.1, "max_tokens": 999999}
			wantCode := 200
			endpoint := "/attempts/attempt_test/v1/chat/completions"
			switch scenario {
			case "truncated":
				wantCode = 503
			case "length":
				wantCode = 502
			case "hosted_tool":
				body["tools"] = []any{map[string]string{"type": "web_search"}}
				wantCode = 403
			case "wrong_grant":
				wantCode = 403
			case "wrong_model":
				body["model"] = "other-model"
				wantCode = 403
			case "stream":
				body["stream"] = true
				wantCode = 400
			case "foreign_history":
				body["previous_response_id"] = "other-run"
				wantCode = 400
			case "remote_image":
				body["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": "https://other.invalid/private"}}}}}
				wantCode = 400
			case "old_endpoint":
				endpoint = "/attempts/attempt_test/v1/responses"
				wantCode = 404
			}
			encoded, _ := json.Marshal(body)
			request := httptest.NewRequest("POST", endpoint, bytes.NewReader(encoded))
			request.Header.Set("Authorization", "Bearer short-grant")
			if scenario == "wrong_grant" {
				request.Header.Set("Authorization", "Bearer wrong")
			}
			response := httptest.NewRecorder()
			k.ServeHTTP(response, request)
			if response.Code != wantCode {
				t.Fatalf("status = %d, want %d", response.Code, wantCode)
			}
			if bytes.Contains(response.Body.Bytes(), []byte("upstream-secret")) {
				t.Fatal("provider key leaked")
			}
			if scenario != "complete" && scenario != "truncated" && scenario != "length" {
				if upstreamCalls != 0 || len(rpc.outcomes) != 0 {
					t.Fatal("forbidden proxy request dispatched")
				}
				return
			}
			want := "succeeded"
			if scenario == "truncated" {
				want = "unknown"
			}
			if scenario == "length" {
				want = "failed"
			}
			if len(rpc.outcomes) != 1 || rpc.outcomes[0] != want {
				t.Fatalf("settlement = %v", rpc.outcomes)
			}
			request = httptest.NewRequest("POST", endpoint, bytes.NewReader(encoded))
			request.Header.Set("Authorization", "Bearer short-grant")
			k.ServeHTTP(httptest.NewRecorder(), request)
			if upstreamCalls != 1 {
				t.Fatal("identical request dispatched twice")
			}
			if scenario == "truncated" {
				body["messages"] = []any{map[string]string{"role": "user", "content": "retry with changed body"}}
				encoded, _ = json.Marshal(body)
				request = httptest.NewRequest("POST", endpoint, bytes.NewReader(encoded))
				request.Header.Set("Authorization", "Bearer short-grant")
				k.ServeHTTP(httptest.NewRecorder(), request)
				if upstreamCalls != 1 {
					t.Fatal("unknown outcome bypassed with a new request")
				}
			}
		})
	}
}

func TestExecutorFileAndProviderBoundaries(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("作品"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "escape.txt")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, path := range []string{"/work/output/escape.txt", "/work/output/../input/file", "/etc/passwd", "/work/output/report.md", "/work/output/session.jsonl"} {
		if f, err := outputFile(root, path); err == nil {
			f.Close()
			t.Fatalf("accepted %s", path)
		}
	}
	f, err := outputFile(root, "/work/output/work.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	configuration := filepath.Join(dir, "provider.json")
	os.WriteFile(configuration, []byte(`{"base_url":"https://model.invalid","protocol":"completion","model":"test","api_key":"fixture"}`), 0644)
	if _, err := completion.LoadProvider(configuration); err == nil {
		t.Fatal("world-readable key accepted")
	}
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	provider, err := completion.NewProvider(completion.ProviderConfig{BaseURL: redirect.URL, Protocol: "completion", Model: "test", APIKey: "fixture"}, redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = provider.Complete(t.Context(), []byte(`{}`))
	if err == nil || redirected != 0 {
		t.Fatal("provider followed credential redirect")
	}
}
