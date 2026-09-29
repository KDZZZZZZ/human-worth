package challengeworker

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
	"unicode/utf8"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge"
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

func TestMaterialToolProtocol(t *testing.T) {
	for _, scenario := range []string{"chinese_pages", "corrupt_digest", "unknown_reference", "invalid_cursor", "tool_limit", "unknown_tool", "incomplete_coverage", "aggregate_limit"} {
		t.Run(scenario, func(t *testing.T) {
			data := []byte(strings.Repeat("中文材料", 9000))
			rpc := &workerRPC{data: data, digest: digest(data)}
			if scenario == "corrupt_digest" {
				rpc.digest = strings.Repeat("0", 64)
			}
			client := clientFor(t, rpc)
			var received strings.Builder
			calls := 0
			model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request completionRequest
				if json.NewDecoder(r.Body).Decode(&request) != nil {
					t.Error("bad request")
					http.Error(w, "bad request", 400)
					return
				}
				calls++
				cursor := ""
				done := false
				last := request.Messages[len(request.Messages)-1]
				if last.Role == "tool" {
					var chunk struct {
						Content    string `json:"content"`
						NextCursor string `json:"nextCursor"`
					}
					if json.Unmarshal([]byte(last.Content.(string)), &chunk) != nil {
						t.Error("bad tool result")
					}
					if !utf8.ValidString(chunk.Content) {
						t.Error("split UTF-8 character")
					}
					received.WriteString(chunk.Content)
					cursor = chunk.NextCursor
					done = cursor == ""
				}
				message := map[string]any{"role": "assistant"}
				reason := "tool_calls"
				if done || scenario == "incomplete_coverage" {
					message["content"] = `{"prompt":"按要求执行"}`
					reason = "stop"
				} else {
					ref := "material"
					name := "read_material"
					if scenario == "unknown_reference" {
						ref = "comment_1"
					}
					if scenario == "unknown_tool" {
						name = "read_labels"
					}
					if scenario == "invalid_cursor" && calls > 1 {
						cursor = "foreign_cursor"
					}
					args, _ := json.Marshal(map[string]string{"materialRef": ref, "cursor": cursor})
					message["tool_calls"] = []any{map[string]any{"id": "call", "type": "function", "function": map[string]string{"name": name, "arguments": string(args)}}}
				}
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": message, "finish_reason": reason}}})
			}))
			defer model.Close()
			provider, err := NewProvider(ProviderConfig{BaseURL: model.URL, Protocol: "completion", Model: "test-model", APIKey: "fixture"}, model.Client())
			if err != nil {
				t.Fatal(err)
			}
			a := &pb.Assignment{Role: "R", Kind: pb.WorkKind_WORK_KIND_REFINE_RANKER, Attempt: &pb.AttemptRef{AttemptId: "attempt_test"}, Model: &pb.ModelConfiguration{Model: "test-model"}, ModelCallLimit: 6, ToolCallLimit: 8, Input: &pb.Assignment_Refine{Refine: &pb.RefineRankerInput{}}, Materials: []*pb.Material{{Id: "material", Asset: &asset.Asset{Id: "asset", Filename: "材料.txt", MediaType: "text/plain", Bytes: int64(len(data)), Sha256: rpc.digest}}}}
			if scenario == "tool_limit" {
				a.ToolCallLimit = 1
			}
			a.InputPolicyHash = challenge.InputPolicyHash(challenge.InputPolicyVersion, a.ModelCallLimit, a.ToolCallLimit)
			if scenario == "aggregate_limit" {
				a.Materials[0].Asset.Bytes = 16 << 20
				a.Materials = append(a.Materials, a.Materials[0])
			}
			worker := &Worker{Client: client, Provider: provider}
			_, err = worker.model(t.Context(), a, "grant")
			if scenario == "aggregate_limit" && calls != 0 {
				t.Fatal("oversized batch dispatched before size validation")
			}
			if scenario == "chinese_pages" {
				if err != nil {
					t.Fatal(err)
				}
				if received.String() != string(data) {
					t.Fatal("tool did not return exact full material")
				}
			} else if err == nil {
				t.Fatal("invalid tool/material accepted")
			}
		})
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
				var request completionRequest
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
			provider, err := NewProvider(ProviderConfig{BaseURL: upstream.URL, Protocol: "completion", Model: "test-model", APIKey: "upstream-secret"}, upstream.Client())
			if err != nil {
				t.Fatal(err)
			}
			parameters, _ := structpb.NewStruct(map[string]any{"temperature": 0.25, "top_p": 0.8})
			a := &pb.Assignment{Attempt: &pb.AttemptRef{AttemptId: "attempt_test"}, Executor: &pb.ExecutorConfiguration{Model: "test-model", Parameters: parameters}, Deadline: timestamppb.New(time.Now().Add(time.Minute))}
			k := &KubernetesExecutor{Client: client, Provider: provider, sessions: map[string]*executorSession{"attempt_test": {assignment: a, grant: "short-grant", activated: true}}}
			body := map[string]any{"model": "test-model", "messages": []any{map[string]string{"role": "user", "content": "OK"}}, "tools": executionTools(), "temperature": 2, "top_p": 0.1, "max_tokens": 999999}
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
	if _, err := LoadProvider(configuration); err == nil {
		t.Fatal("world-readable key accepted")
	}
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected++ }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	provider, err := NewProvider(ProviderConfig{BaseURL: redirect.URL, Protocol: "completion", Model: "test", APIKey: "fixture"}, redirect.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = provider.complete(t.Context(), []byte(`{}`))
	if err == nil || redirected != 0 {
		t.Fatal("provider followed credential redirect")
	}
}
