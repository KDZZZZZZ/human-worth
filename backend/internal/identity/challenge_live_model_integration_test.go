//go:build integration && live

package identity

import (
	"context"
	"crypto/tls"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/completion"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/execution"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/rpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge/agent"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestBackendChallengeLiveModel(t *testing.T) {
	if os.Getenv("CHALLENGE_MODEL_CONFIG_FILE") == "" {
		t.Skip("requires an explicitly configured real model")
	}
	for _, task := range []liveChallengeTask{
		{name: "file-regression", filename: "result.txt", description: "Create /work/output/result.txt containing exactly HUMAN_WORTH_LIVE_GROK_OK followed by a single newline. Use run_command to create and verify this file.", requirements: "Deliver exactly one text/plain file at /work/output/result.txt with those exact bytes. Do not return an inline text artifact.", expected: "HUMAN_WORTH_LIVE_GROK_OK\n"},
		{name: "sales-csv", filename: "category-summary.csv", description: "Read the attached UTF-8 sales CSV from the supplied local file manifest. For each row, net revenue is quantity * unit_price - discount; discount applies once per row. Sum net revenue by category using exact decimal arithmetic. Sort categories alphabetically and save /work/output/category-summary.csv. Use run_command to read the attachment, calculate and verify the result.", requirements: "Deliver one CSV file with header category,total_net, one row per category, and totals formatted with exactly two decimal places. Include no commentary or totals row in the CSV.", input: "category,quantity,unit_price,discount\nbooks,2,12.50,1.00\ngames,1,40.00,5.00\nbooks,3,8.00,0.50\nmusic,4,3.25,1.00\n", expected: "category,total_net\nbooks,47.50\ngames,35.00\nmusic,12.00\n"},
	} {
		t.Run(task.name, func(t *testing.T) { runBackendChallengeLiveTask(t, task) })
	}
}

type liveChallengeTask struct {
	name, filename, description, requirements, input, expected string
}

func runBackendChallengeLiveTask(t *testing.T, task liveChallengeTask) {
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
	provider, e := completion.LoadProvider(os.Getenv("CHALLENGE_MODEL_CONFIG_FILE"))
	must(t, e)
	f := newBackendLab(t, provider.Model())
	var attachments []string
	if task.input != "" {
		attachments = append(attachments, f.upload(t, f.alice, "live-task-input-0001", []byte(task.input), true))
	}
	target := publishLiveArithmeticTask(t, f, "live-model-target-0001", task.name, task.description, task.expected, "WRONG", attachments...)
	for i, pair := range [][2]string{{"2 + 2", "4"}, {"3 + 3", "6"}} {
		task := publishLiveArithmeticTask(t, f, fmt.Sprintf("live-grok-sample-%02d", i), "Arithmetic accuracy", "Compute "+pair[0]+". The correct integer answer is required.", pair[1], "999")
		ballot := f.ballot(t, f.alice, task.ID)
		chosen := ""
		for _, entry := range ballot.Entries {
			if entry.Side == "human" {
				chosen = entry.ID
			}
		}
		if chosen == "" {
			t.Fatal("missing correct human fixture")
		}
		f.request(t, 0, f.alice, "PUT", "/api/tasks/"+task.ID+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, ballot.ID, chosen), nil, 200).Body.Close()
	}
	// Use a restricted ServiceAccount for Job/Secret operations. The kubeconfig
	// is outside Git, mode 0600, and is never sent to the executor Pod.
	var cfg map[string]any
	must(t, json.Unmarshal(kube("", "config", "view", "--raw", "--minify", "-o", "json"), &cfg))
	token := strings.TrimSpace(string(kube("", "-n", "human-worth", "create", "token", "challenge-worker", "--duration=30m")))
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
		c := exec.Command("kubectl", "--kubeconfig", kubeconfig, "-n", "human-worth", "delete", "pod", "sandbox-relay", "--ignore-not-found", "--wait=true", "--timeout=30s")
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
	configuration := &pb.RunConfiguration{InitialTaskPackage: &pb.InitialTaskPackage{TaskRevision: 1, TaskAttachmentIds: attachments, Description: task.description, WorkRequirements: task.requirements}, Packer: &pb.ModelConfiguration{Model: provider.Model()}, Ranker: &pb.ModelConfiguration{Model: provider.Model()}, Executor: &pb.ExecutorConfiguration{Model: provider.Model(), Harness: "completion-shell"}, RankerFitThreshold: .8, RankerMinComparablePairs: 1, RankerRoundLimit: 1, RoundLimit: 1, Budget: &pb.Budget{Amount: 20, Unit: "model_calls"}}
	cfgJSON, e := protojson.Marshal(configuration)
	must(t, e)
	run := flowJSON(t, f.request(t, 0, f.admin, "POST", "/api/admin/challenge-runs", fmt.Sprintf(`{"taskId":%q,"configuration":%s}`, target.ID, cfgJSON), map[string]string{"Idempotency-Key": "sandbox-complete-run"}, 202))
	runID := run["id"].(string)
	worker := &agent.Worker{Client: rpc.WorkerClient{Client: f.workers[0]}, Codec: protobuf.AgentPayloads{}, Provider: provider, Executor: k, Instance: "sandbox-worker"}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	registeredViaHTTP := false
	registrationPath := ""
	registrationEntry := ""
	for step := 0; step < 30; step++ {
		must(t, f.challenges[step%2].Reconcile(ctx))
		run = flowJSON(t, f.request(t, 1, f.admin, "GET", "/api/admin/challenge-runs/"+runID, "", nil, 200))
		t.Logf("real model run: status=%v stage=%v", run["status"], run["stage"])
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
		after := flowJSON(t, f.request(t, 0, f.admin, "GET", "/api/admin/challenge-runs/"+runID, "", nil, 200))
		if after["stage"] == "registering" && !registeredViaHTTP {
			candidates := after["candidates"].([]any)
			if len(candidates) != 1 {
				t.Fatal("expected one real executor candidate", after)
			}
			candidateID := candidates[0].(map[string]any)["id"].(string)
			registrationPath = "/api/admin/challenge-runs/" + runID + "/candidates/" + candidateID + "/registration"
			f.request(t, 0, f.alice, "PUT", registrationPath, "", nil, 403).Body.Close()
			for i, want := range []int{201, 200} {
				receipt := flowJSON(t, f.request(t, i, f.admin, "PUT", registrationPath, "", nil, want))
				if receipt["created"] != (want == 201) || receipt["reviewState"] != "pending_review" || receipt["entryId"] == "" || receipt["runId"] != runID || receipt["candidateId"] != candidateID {
					t.Fatal("invalid real registration receipt", receipt)
				}
				if i == 0 {
					registrationEntry = receipt["entryId"].(string)
				} else if receipt["entryId"] != registrationEntry {
					t.Fatal("registration replay created duplicate entry")
				}
			}
			registeredViaHTTP = true
		}

	}
	if run["status"] != "completed" {
		t.Fatalf("did not finish: %v", run)
	}
	if !registeredViaHTTP {
		t.Fatal("did not exercise real HTTP registration")
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
	if task.input == "" {
		if string(data) != task.expected {
			t.Fatal("registered artifact bytes changed")
		}
	} else {
		actual, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
		must(t, err)
		expected, err := csv.NewReader(strings.NewReader(task.expected)).ReadAll()
		must(t, err)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("incorrect sales aggregation: got %v, expected %v", actual, expected)
		}
	}
	if len(f.ballot(t, f.bob, target.ID).Entries) != 3 {
		t.Fatal("reviewed sandbox output absent")
	}
	approvedReceipt := flowJSON(t, f.request(t, 1, f.admin, "PUT", registrationPath, "", nil, 200))
	if approvedReceipt["entryId"] != registrationEntry || approvedReceipt["created"] != false || approvedReceipt["reviewState"] != "approved" {
		t.Fatal("registration replay lost current reviewed state", approvedReceipt)
	}
	var jobs struct{ Items []any }
	must(t, json.Unmarshal(kube("", "-n", "human-worth-execution", "get", "jobs", "-o", "json"), &jobs))
	if len(jobs.Items) != 0 {
		t.Fatal("execution Job not cleaned up")
	}
	if directory := os.Getenv("CHALLENGE_LIVE_OUTPUT_DIR"); directory != "" {
		must(t, os.MkdirAll(directory, 0700))
		must(t, os.WriteFile(filepath.Join(directory, task.filename), data, 0600))
		evidence, err := json.MarshalIndent(map[string]any{"model": provider.Model(), "taskId": target.ID, "run": run, "registration": approvedReceipt, "assetId": assetID, "artifact": task.filename, "artifactBytes": len(data), "reviewedBallotEntries": 3}, "", "  ")
		must(t, err)
		must(t, os.WriteFile(filepath.Join(directory, task.name+"-run.json"), evidence, 0600))
	}
	t.Logf("real %s + real services + gVisor: %s verified, HTTP registration/replay verified, reviewed, downloaded and added to ballot", provider.Model(), task.name)
}

func publishLiveArithmeticTask(t *testing.T, f *backendLab, key, title, description, correct, wrong string, attachments ...string) draftResult {
	t.Helper()
	entries := []any{}
	for i, answer := range []string{correct, wrong} {
		side := "human"
		if i == 1 {
			side = "agent"
		}
		entry := map[string]any{"side": side, "title": side + " answer", "source": "synthetic test fixture", "artifacts": []any{map[string]any{"kind": "text", "text": answer}}, "permissions": map[string]any{"cloudUse": true}}
		if i == 1 {
			entry["agentConfiguration"] = map[string]any{"model": "synthetic-fixture", "prompt": "test fixture"}
		}
		entries = append(entries, entry)
	}
	body := map[string]any{"title": title, "summary": "Synthetic live model validation", "description": description, "category": "writing", "entries": entries}
	if len(attachments) != 0 {
		files := []any{}
		for _, id := range attachments {
			files = append(files, map[string]any{"assetId": id, "cloudUseAllowed": true})
		}
		body["taskAttachments"] = files
	}
	raw, e := json.Marshal(body)
	must(t, e)
	var task draftResult
	readJSON(t, f.request(t, 0, f.alice, "POST", "/api/tasks", string(raw), map[string]string{"Idempotency-Key": key}, 201), &task)
	readJSON(t, f.request(t, 1, f.alice, "POST", "/api/me/tasks/"+task.ID+"/review-requests", `{"expectedRevision":1}`, nil, 200), &task)
	_, e = f.contents[0].DeliverReview(t.Context(), f.senders[1])
	must(t, e)
	f.decide(t, *task.ReviewID, "approve", "synthetic live model fixture")
	must(t, f.contents[0].BootstrapCatalogs(t.Context()))
	return task
}
