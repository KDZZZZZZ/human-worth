//go:build integration

package identity

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/completion"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/execution"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/rpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type checkedUploadClient struct {
	pb.ChallengeServiceClient
	t *testing.T
}
type checkedUploadStream struct {
	grpc.ClientStreamingClient[pb.UploadArtifactRequest, pb.UploadArtifactResponse]
	t *testing.T
}

func (c checkedUploadClient) UploadArtifact(ctx context.Context, options ...grpc.CallOption) (grpc.ClientStreamingClient[pb.UploadArtifactRequest, pb.UploadArtifactResponse], error) {
	s, e := c.ChallengeServiceClient.UploadArtifact(ctx, options...)
	return checkedUploadStream{s, c.t}, e
}
func (s checkedUploadStream) CloseAndRecv() (*pb.UploadArtifactResponse, error) {
	v, e := s.ClientStreamingClient.CloseAndRecv()
	if e != nil {
		s.t.Logf("real artifact upload: %v", e)
	}
	return v, e
}

// Model responses are deterministic fixtures, not fake business services or a
// fake executor. E runs the real command tool in a real gVisor Kubernetes Job.
// The model intentionally probes the sandbox and reports the command result.
func TestBackendChallengeRealSandbox(t *testing.T) {
	kubeconfig := os.Getenv("CHALLENGE_E2E_KUBECONFIG")
	if kubeconfig == "" {
		t.Skip("requires dedicated kind-human-worth-backend-check, Calico and runsc")
	}
	image := os.Getenv("CHALLENGE_E2E_IMAGE")
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("pinned local executor image required")
	}
	kube := func(input string, args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
		defer cancel()
		c := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", kubeconfig, "--context", "kind-human-worth-backend-check"}, args...)...)
		c.Stdin = strings.NewReader(input)
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("temporary cluster command %v: %v: %s", args, e, b)
		}
		return b
	}
	// Prevent this test from mutating the live lab even if its environment is wrong.
	var cluster struct{ Contexts []struct{ Name string } }
	must(t, json.Unmarshal(kube("", "config", "view", "-o", "json"), &cluster))
	if len(cluster.Contexts) != 1 || cluster.Contexts[0].Name != "kind-human-worth-backend-check" {
		t.Fatal("isolated kubeconfig required")
	}
	kube(`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"human-worth"}}`, "apply", "-f", "-")
	isolation, e := os.ReadFile("../../../ops/challenge/execution-isolation.yaml")
	must(t, e)
	kube(string(isolation), "apply", "-f", "-")
	f := newBackendLab(t)
	// Three independent published tasks, with real account votes. Training may
	// use one; validation must use the other, never the target or training task.
	target := f.publishTask(t, "sandbox-target-task", "")
	preferred := map[string]bool{}
	for i := 0; i < 2; i++ {
		task := f.publishTask(t, fmt.Sprintf("sandbox-training-%02d", i), "")
		b := f.ballot(t, f.alice, task.ID)
		chosen := b.Entries[0].ID
		preferred[chosen] = true
		f.request(t, 0, f.alice, "PUT", "/api/tasks/"+task.ID+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, b.ID, chosen), nil, 200).Body.Close()
	}
	var mu sync.Mutex
	commandObserved := false
	modelCalls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-private-model-key" {
			http.Error(w, "forbidden", 403)
			return
		}
		var request agent.CompletionRequest
		if json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(&request) != nil || len(request.Messages) < 2 {
			http.Error(w, "invalid", 400)
			return
		}
		mu.Lock()
		modelCalls++
		mu.Unlock()
		system, _ := request.Messages[0].Content.(string)
		raw, _ := request.Messages[1].Content.(string)
		var result proto.Message
		content := ""
		switch {
		case strings.Contains(system, "执行管理员任务包"):
			if strings.Contains(raw, "original work") || strings.Contains(raw, "private-config") {
				t.Error("E received competitor content")
			}
			last := request.Messages[len(request.Messages)-1]
			if last.Role != "tool" {
				// No token, no host root writes, no Kubernetes API route. The
				// only allowed egress is the private attempt relay and DNS.
				script := `python - <<'PY'
import os,pathlib,socket
assert os.getuid()==10001
assert not pathlib.Path('/var/run/secrets/kubernetes.io/serviceaccount/token').exists()
assert not pathlib.Path('/etc/shadow').exists() or not os.access('/etc/shadow',os.R_OK)
try:
 pathlib.Path('/forbidden-write').write_text('x')
 raise AssertionError('root filesystem writable')
except OSError: pass
for address in [('kubernetes.default.svc.cluster.local',443),('1.1.1.1',443)]:
 try:
  s=socket.create_connection(address,timeout=2);s.close()
  raise AssertionError('network isolation bypass')
 except (OSError,socket.timeout): pass
pathlib.Path('/work/output/result.txt').write_text('sandbox generated result\n')
print('SANDBOX_COMMAND_PASS')
PY`
				arguments, _ := json.Marshal(map[string]string{"command": script})
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "sandbox_tool_1", "type": "function", "function": map[string]string{"name": "run_command", "arguments": string(arguments)}}}}}}})
				return
			}
			observed, _ := last.Content.(string)
			if !strings.Contains(observed, "SANDBOX_COMMAND_PASS") {
				t.Errorf("sandbox command failed: %s", observed)
				http.Error(w, "failed", 500)
				return
			}
			mu.Lock()
			commandObserved = true
			mu.Unlock()
			content = `{"artifacts":[{"kind":"file","path":"/work/output/result.txt","mediaType":"text/plain"}],"report":"Created and checked the result file inside the isolated workspace."}`
		case strings.Contains(system, "本轮用于判断的完整标准"):
			result = &pb.PromptResult{Prompt: "Compare completeness and accuracy of the work itself."}
		case strings.Contains(system, "改进后的执行提示"):
			result = &pb.PromptResult{Prompt: "Create the requested output and verify its contents."}
		case strings.Contains(system, "仅依据任务和自身作品"):
			in := &pb.ExplainInput{}
			if e := protojson.Unmarshal([]byte(strings.Split(raw, "\n文件清单：")[0]), in); e != nil {
				t.Error(e)
				http.Error(w, "bad explain", 400)
				return
			}
			result = &pb.Judgment{WorkId: in.OwnWork.Id, Criteria: "complete", Rationale: "Output matches the initial requirements", EvidenceRefs: []string{in.OwnWork.Id}}
		default:
			in := &pb.RankInput{}
			if e := protojson.Unmarshal([]byte(strings.Split(raw, "\n文件清单：")[0]), in); e != nil {
				t.Error(e)
				http.Error(w, "bad rank", 400)
				return
			}
			if strings.Contains(raw, "humanCounts") {
				t.Error("ranker labels leaked")
			}
			ranks := &pb.Rankings{}
			for _, sample := range in.Samples {
				entry := &pb.Ranking{TaskId: sample.Task.TaskId}
				for _, work := range sample.Works {
					entry.OrderedWorkIds = append(entry.OrderedWorkIds, work.Id)
					entry.Judgments = append(entry.Judgments, &pb.Judgment{WorkId: work.Id, Criteria: "complete", Rationale: "Compared the submitted work", EvidenceRefs: []string{work.Id}})
				}
				sort.SliceStable(entry.OrderedWorkIds, func(i, j int) bool { return preferred[entry.OrderedWorkIds[i]] && !preferred[entry.OrderedWorkIds[j]] })
				ranks.Items = append(ranks.Items, entry)
			}
			result = ranks
		}
		if result != nil {
			b, e := protojson.Marshal(result)
			if e != nil {
				t.Error(e)
				return
			}
			content = string(b)
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": content}}}})
	}))
	t.Cleanup(model.Close)
	provider, e := completion.NewProvider(completion.ProviderConfig{BaseURL: model.URL, Model: "fixture-model", Protocol: "completion", APIKey: "fixture-private-model-key"}, model.Client())
	must(t, e)
	// Use a restricted ServiceAccount for Job/Secret operations. The kubeconfig
	// is outside Git, mode 0600, and is never sent to the executor Pod.
	var cfg map[string]any
	must(t, json.Unmarshal(kube("", "config", "view", "--raw", "--minify", "-o", "json"), &cfg))
	token := strings.TrimSpace(string(kube("", "-n", "human-worth", "create", "token", "challenge-worker", "--duration=10m")))
	cfg["users"] = []any{map[string]any{"name": "worker", "user": map[string]any{"token": token}}}
	cfg["contexts"] = []any{map[string]any{"name": "worker", "context": map[string]any{"cluster": cfg["clusters"].([]any)[0].(map[string]any)["name"], "user": "worker", "namespace": "human-worth-execution"}}}
	cfg["current-context"] = "worker"
	workerConfig := filepath.Join(t.TempDir(), "worker-kubeconfig")
	rawCfg, _ := json.Marshal(cfg)
	must(t, os.WriteFile(workerConfig, rawCfg, 0600))
	cert, key, ca := f.identity.pki.cert(t, "challenge-worker")
	pair, e := tls.LoadX509KeyPair(cert, key)
	must(t, e)
	listener, e := net.Listen("tcp", "0.0.0.0:0")
	must(t, e)
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	// A pure TCP forwarder puts the host test's relay behind the same Pod-label
	// boundary used in deployment. It sees no TLS plaintext or business secrets.
	var network []struct {
		IPAM struct{ Config []struct{ Gateway string } }
	}
	networkRaw, e := exec.Command("docker", "network", "inspect", "kind").Output()
	must(t, e)
	must(t, json.Unmarshal(networkRaw, &network))
	host := ""
	for _, item := range network[0].IPAM.Config {
		if ip := net.ParseIP(item.Gateway); ip != nil && ip.To4() != nil {
			host = item.Gateway
			break
		}
	}
	if host == "" {
		t.Fatal("IPv4 Docker bridge required")
	}
	proxyScript := `import socket,threading,os
target=(os.environ['TARGET_HOST'],int(os.environ['TARGET_PORT']))
def copy(a,b):
 try:
  while True:
   data=a.recv(65536)
   if not data:break
   b.sendall(data)
 except OSError:pass
 finally:
  try:b.shutdown(socket.SHUT_WR)
  except OSError:pass
def serve(c):
 try:
  out=socket.create_connection(target,timeout=10);out.settimeout(None)
  a=threading.Thread(target=copy,args=(c,out));a.start();copy(out,c);a.join();out.close()
 finally:c.close()
s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('0.0.0.0',9443));s.listen()
while True:
 c,_=s.accept();threading.Thread(target=serve,args=(c,),daemon=True).start()
`
	proxy := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "sandbox-relay", "namespace": "human-worth", "labels": map[string]string{"app": "challenge-worker"}}, "spec": map[string]any{"automountServiceAccountToken": false, "securityContext": map[string]any{"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "seccompProfile": map[string]string{"type": "RuntimeDefault"}}, "containers": []any{map[string]any{"name": "relay", "image": image, "command": []string{"python", "-u", "-c", proxyScript}, "env": []any{map[string]string{"name": "TARGET_HOST", "value": host}, map[string]string{"name": "TARGET_PORT", "value": port}}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true, "capabilities": map[string]any{"drop": []string{"ALL"}}}, "readinessProbe": map[string]any{"tcpSocket": map[string]any{"port": 9443}, "periodSeconds": 1}, "resources": map[string]any{"requests": map[string]string{"cpu": "25m", "memory": "32Mi"}, "limits": map[string]string{"cpu": "200m", "memory": "128Mi"}}}}}}
	proxyRaw, _ := json.Marshal(proxy)
	kube(string(proxyRaw), "apply", "-f", "-")
	t.Cleanup(func() {
		c := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", "human-worth", "delete", "pod", "sandbox-relay", "--ignore-not-found", "--wait=false")
		_ = c.Run()
	})
	kube("", "-n", "human-worth", "wait", "--for=condition=Ready", "pod/sandbox-relay", "--timeout=40s")
	ip := strings.TrimSpace(string(kube("", "-n", "human-worth", "get", "pod", "sandbox-relay", "-o", "jsonpath={.status.podIP}")))
	k := &execution.KubernetesExecutor{Profile: execution.ExecutorProfile{Kubeconfig: workerConfig, Context: "worker", Namespace: "human-worth-execution", Image: image, RuntimeClass: "gvisor", RelayURL: "https://" + net.JoinHostPort(ip, "9443"), CAFile: ca}, Client: checkedUploadClient{f.workers[0], t}, Provider: provider, Instance: "sandbox-worker"}
	must(t, k.Preflight(t.Context()))
	must(t, k.Cleanup(t.Context()))
	relayHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		k.ServeHTTP(rec, r)
		if rec.Code >= 400 {
			t.Logf("relay %s %s: %d %s", r.Method, r.URL.Path, rec.Code, rec.Body.String())
		}
		for key, values := range rec.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
	relay := &http.Server{Handler: relayHandler, TLSConfig: &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}}
	go relay.ServeTLS(listener, "", "")
	t.Cleanup(func() { relay.Close() })
	configuration := &pb.RunConfiguration{InitialTaskPackage: &pb.InitialTaskPackage{TaskRevision: 1, Description: "Produce a text file", WorkRequirements: "A complete result file"}, Packer: &pb.ModelConfiguration{Model: "fixture-model"}, Ranker: &pb.ModelConfiguration{Model: "fixture-model"}, Executor: &pb.ExecutorConfiguration{Model: "fixture-model", Harness: "completion-shell"}, RankerFitThreshold: .8, RankerMinComparablePairs: 1, RankerRoundLimit: 1, RoundLimit: 1, Budget: &pb.Budget{Amount: 30, Unit: "model_calls"}}
	cfgJSON, e := protojson.Marshal(configuration)
	must(t, e)
	run := flowJSON(t, f.request(t, 0, f.admin, "POST", "/api/admin/challenge-runs", fmt.Sprintf(`{"taskId":%q,"configuration":%s}`, target.ID, cfgJSON), map[string]string{"Idempotency-Key": "sandbox-complete-run"}, 202))
	runID := run["id"].(string)
	worker := &agent.Worker{Client: rpc.WorkerClient{Client: f.workers[0]}, Codec: protobuf.AgentPayloads{}, Provider: provider, Executor: k, Instance: "sandbox-worker"}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	for step := 0; step < 30; step++ {
		must(t, f.challenges[step%2].Reconcile(ctx))
		run = flowJSON(t, f.request(t, 1, f.admin, "GET", "/api/admin/challenge-runs/"+runID, "", nil, 200))
		if run["status"] == "completed" {
			break
		}
		if run["status"] == "failed" {
			t.Fatalf("run failed: %v", run)
		}
		_, e = worker.RunOnce(ctx)
		if e != nil {
			t.Logf("relay forwarder: %s", kube("", "-n", "human-worth", "logs", "sandbox-relay"))
			t.Fatalf("worker step %d (%v): %v", step, run["stage"], e)
		}
	}
	if run["status"] != "completed" {
		t.Fatalf("did not finish: %v", run)
	}
	mu.Lock()
	observed, calls := commandObserved, modelCalls
	mu.Unlock()
	if !observed || calls < 5 {
		t.Fatal("did not exercise all roles and real command tool")
	}
	var entry, review string
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT s.id,s.review_id FROM content.entry_submissions s JOIN content.challenge_registrations r ON r.entry_id=s.id WHERE r.run_id=$1`, runID).Scan(&entry, &review))
	if len(f.ballot(t, f.bob, target.ID).Entries) != 2 {
		t.Fatal("unreviewed cloud result leaked")
	}
	f.deliver(t)
	f.decide(t, review, "approve", "sandbox output verified")
	must(t, f.contents[1].BootstrapCatalogs(t.Context()))
	var assetID string
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT content->'artifacts'->0->>'assetId' FROM content.entry_submissions WHERE id=$1`, entry).Scan(&assetID))
	r := f.request(t, 1, additionalActor{}, "GET", "/api/tasks/"+target.ID+"/entries/"+entry+"/assets/"+assetID+"/content", "", nil, 200)
	data, e := io.ReadAll(r.Body)
	r.Body.Close()
	must(t, e)
	if string(data) != "sandbox generated result\n" {
		t.Fatal("registered artifact bytes changed")
	}
	if len(f.ballot(t, f.bob, target.ID).Entries) != 3 {
		t.Fatal("reviewed sandbox output absent")
	}
	var jobs struct{ Items []any }
	must(t, json.Unmarshal(kube("", "-n", "human-worth-execution", "get", "jobs", "-o", "json"), &jobs))
	if len(jobs.Items) != 0 {
		t.Fatal("execution Job not cleaned up")
	}
	t.Logf("real services + gVisor + P/R/E Completion protocol: %d model calls; output reviewed, downloaded, and added to ballot", calls)
}
