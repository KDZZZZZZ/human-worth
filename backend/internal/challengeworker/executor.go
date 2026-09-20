package challengeworker

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// ExecutorProfile 由可信运维配置，不接受管理员请求中的任意镜像、命令或网络地址。
type ExecutorProfile struct {
	Kubeconfig   string `json:"kubeconfig"`
	Context      string `json:"context"`
	Namespace    string `json:"namespace"`
	Image        string `json:"image"`
	RuntimeClass string `json:"runtime_class"`
	RelayURL     string `json:"relay_url"`
	CAFile       string `json:"ca_file"`
}
type executorSession struct {
	assignment       *pb.Assignment
	grant            string
	activated        bool
	result           *pb.GeneratedWork
	inflight         int
	settlementFailed bool
}

// KubernetesExecutor 管理 E 的固定 Job；模型主密钥仅用于本进程的受控 Completion 中继。
type KubernetesExecutor struct {
	Profile  ExecutorProfile
	Client   pb.ChallengeServiceClient
	Provider *Provider
	Instance string
	mu       sync.Mutex
	sessions map[string]*executorSession
}

func LoadExecutorProfile(path string) (ExecutorProfile, error) {
	var p ExecutorProfile
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 16384 {
		return p, errors.New("executor profile unavailable")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF {
		return p, errors.New("invalid executor profile")
	}
	u, err := url.Parse(p.RelayURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Port() != "9443" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || p.Kubeconfig == "" || p.Context == "" || p.Namespace == "" || p.RuntimeClass != "gvisor" && p.RuntimeClass != "runsc" || !strings.Contains(p.Image, "@sha256:") || p.CAFile == "" {
		return p, errors.New("executor requires pinned image, isolated runtime and HTTPS relay")
	}
	sum, err := hex.DecodeString(strings.Split(p.Image, "@sha256:")[1])
	if err != nil || len(sum) != 32 {
		return p, errors.New("executor image digest required")
	}
	return p, nil
}

// IsolationPolicy 是 E 命名空间唯一允许的网络策略，部署时同时要求支持 NetworkPolicy 的 CNI。
func IsolationPolicy() map[string]any {
	var spec map[string]any
	_ = json.Unmarshal([]byte(`{"podSelector":{},"policyTypes":["Ingress","Egress"],"egress":[{"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"human-worth"}},"podSelector":{"matchLabels":{"app":"challenge-worker"}}}],"ports":[{"protocol":"TCP","port":9443}]},{"to":[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"k8s-app":"kube-dns"}}}],"ports":[{"protocol":"UDP","port":53},{"protocol":"TCP","port":53}]}]}`), &spec)
	return spec
}

// Preflight 拒绝宽松策略或其他叠加放行策略；运维配置只能指定已有隔离运行时。
func (k *KubernetesExecutor) Preflight(ctx context.Context) error {
	if k.Provider == nil || k.Provider.config.Protocol != "completion" {
		return errors.New("executor requires completion protocol")
	}
	if k.Instance == "" {
		return errors.New("executor instance required")
	}
	b, err := k.kubectl(ctx, nil, "get", "runtimeclass", k.Profile.RuntimeClass, "-o", "json")
	if err != nil {
		return err
	}
	var runtime struct {
		Handler string `json:"handler"`
	}
	if json.Unmarshal(b, &runtime) != nil || runtime.Handler != "runsc" {
		return errors.New("gVisor runtime required")
	}
	b, err = k.kubectl(ctx, nil, "get", "networkpolicy", "-o", "json")
	if err != nil {
		return err
	}
	var policies struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec map[string]any `json:"spec"`
		} `json:"items"`
	}
	if json.Unmarshal(b, &policies) != nil || len(policies.Items) != 1 || policies.Items[0].Metadata.Name != "challenge-execution-isolation" {
		return errors.New("dedicated execution network policy required")
	}
	actual, _ := json.Marshal(policies.Items[0].Spec)
	expected, _ := json.Marshal(IsolationPolicy())
	if !bytes.Equal(actual, expected) {
		return errors.New("execution network policy mismatch")
	}
	return nil
}

// Cleanup 在同一 worker 实例重启后回收遗留 Job；失败时不领取新的 E 工作。
func (k *KubernetesExecutor) Cleanup(ctx context.Context) error {
	if k.Instance == "" {
		return errors.New("executor instance required")
	}
	_, err := k.kubectl(ctx, nil, "delete", "jobs", "-l", "human-worth-worker="+digest([]byte(k.Instance))[:32], "--cascade=foreground", "--wait=true", "--timeout=10s")
	return err
}
func (k *KubernetesExecutor) kubectl(ctx context.Context, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "kubectl", append([]string{"--kubeconfig", k.Profile.Kubeconfig, "--context", k.Profile.Context, "--namespace", k.Profile.Namespace, "--request-timeout=10s"}, args...)...)
	command.Stdin = bytes.NewReader(input)
	var out limitedBuffer
	out.limit = 2 << 20
	command.Stdout = &out
	command.Stderr = io.Discard
	if command.Run() != nil {
		return nil, errors.New("executor cluster operation failed")
	}
	return out.buffer.Bytes(), nil
}

type limitedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		return 0, errors.New("output limit exceeded")
	}
	return b.buffer.Write(p)
}

// Execute 只创建固定安全模板，Job 创建结果不明时核对同名对象，不新建第二份执行。
func (k *KubernetesExecutor) Execute(ctx context.Context, a *pb.Assignment, grant string) (result *pb.GeneratedWork, resultErr error) {
	if a.Executor == nil || k.Provider == nil || a.Executor.Harness != "completion-shell" || a.Executor.Model != k.Provider.config.Model || k.Provider.config.Protocol != "completion" || len(a.Executor.Skills) != 0 {
		return nil, errors.New("compatible completion executor configuration required")
	}
	if err := k.Preflight(ctx); err != nil {
		return nil, err
	}
	name := "challenge-" + strings.TrimPrefix(a.Attempt.AttemptId, "attempt_")
	session := &executorSession{assignment: a, grant: grant}
	k.mu.Lock()
	if k.sessions == nil {
		k.sessions = map[string]*executorSession{}
	}
	k.sessions[a.Attempt.AttemptId] = session
	k.mu.Unlock()
	defer func() { k.mu.Lock(); delete(k.sessions, a.Attempt.AttemptId); k.mu.Unlock() }()
	ca, err := os.ReadFile(k.Profile.CAFile)
	if err != nil {
		return nil, errors.New("executor CA unavailable")
	}
	input, _ := protojson.Marshal(a)
	// 先建 Job，再将 Secret 绑定为它的子资源；中断时 Job 的 TTL 仍能回收凭据。
	owned := false
	defer func() {
		if !owned {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if _, err := k.kubectl(cleanup, nil, "delete", "job", name, "--ignore-not-found=true", "--cascade=foreground", "--wait=true", "--timeout=10s"); err != nil {
			result = nil
			resultErr = errors.New("executor cleanup pending")
		}
	}()
	seconds := int64(time.Until(a.Deadline.AsTime()).Seconds())
	if seconds <= 0 {
		return nil, errors.New("executor deadline exceeded")
	}
	// 只有执行目录与临时目录可写；镜像、命令、身份、网络运行时和资源来自固定模板。
	pod := map[string]any{
		"runtimeClassName":              k.Profile.RuntimeClass,
		"restartPolicy":                 "Never",
		"automountServiceAccountToken":  false,
		"terminationGracePeriodSeconds": 5,
		"securityContext": map[string]any{
			"runAsNonRoot": true, "runAsUser": 10001, "runAsGroup": 10001, "fsGroup": 10001,
			"seccompProfile": map[string]string{"type": "RuntimeDefault"},
		},
		"containers": []any{map[string]any{
			"name": "executor", "image": k.Profile.Image,
			"command": []string{"/usr/local/bin/challenge-executor"},
			"env": []any{
				map[string]string{"name": "CHALLENGE_RELAY_URL", "value": k.Profile.RelayURL},
				map[string]string{"name": "SSL_CERT_FILE", "value": "/control/ca.crt"},
			},
			"securityContext": map[string]any{
				"allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
				"capabilities": map[string]any{"drop": []string{"ALL"}},
			},
			"resources": map[string]any{
				"requests": map[string]string{"cpu": "250m", "memory": "512Mi"},
				"limits":   map[string]string{"cpu": "1", "memory": "2Gi", "ephemeral-storage": "1Gi"},
			},
			"volumeMounts": []any{
				map[string]any{"name": "control", "mountPath": "/control", "readOnly": true},
				map[string]any{"name": "work", "mountPath": "/work"},
				map[string]any{"name": "tmp", "mountPath": "/tmp"},
			},
		}},
		"volumes": []any{
			map[string]any{"name": "control", "secret": map[string]any{"secretName": name, "defaultMode": 0440}},
			map[string]any{"name": "work", "emptyDir": map[string]string{"sizeLimit": "1Gi"}},
			map[string]any{"name": "tmp", "emptyDir": map[string]string{"sizeLimit": "128Mi"}},
		},
	}
	job := map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": name, "labels": map[string]string{
			"human-worth-attempt": a.Attempt.AttemptId,
			"human-worth-worker":  digest([]byte(k.Instance))[:32],
		}},
		"spec": map[string]any{
			"backoffLimit": 0, "activeDeadlineSeconds": seconds, "ttlSecondsAfterFinished": 300,
			"template": map[string]any{
				"metadata": map[string]any{"labels": map[string]string{"app": "challenge-executor"}},
				"spec":     pod,
			},
		},
	}
	data, _ := json.Marshal(job)
	expected := digest(data)
	// 模板摘要用于创建响应丢失后的对账，不从 Pod 返回的自报信息决定可信配置。
	job["metadata"].(map[string]any)["annotations"] = map[string]string{"human-worth-template": expected}
	data, _ = json.Marshal(job)
	if _, err = k.kubectl(ctx, data, "create", "-f", "-"); err != nil {
		existing, e := k.kubectl(ctx, nil, "get", "job", name, "-o", "json")
		if e != nil {
			return nil, err
		}
		var object struct {
			Metadata struct {
				Annotations map[string]string `json:"annotations"`
			} `json:"metadata"`
		}
		if json.Unmarshal(existing, &object) != nil || object.Metadata.Annotations["human-worth-template"] != expected {
			return nil, errors.New("executor job conflict")
		}
	}
	owned = true
	object, err := k.kubectl(ctx, nil, "get", "job", name, "-o", "json")
	if err != nil {
		return nil, err
	}
	var identity struct {
		Metadata struct {
			UID string `json:"uid"`
		} `json:"metadata"`
	}
	if json.Unmarshal(object, &identity) != nil || identity.Metadata.UID == "" {
		return nil, errors.New("executor job identity unavailable")
	}
	secret := map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": name, "uid": identity.Metadata.UID, "controller": true}}}, "stringData": map[string]string{"assignment.json": string(input), "grant": grant, "ca.crt": string(ca)}}
	data, _ = json.Marshal(secret)
	if _, err = k.kubectl(ctx, data, "create", "-f", "-"); err != nil {
		return nil, err
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			k.mu.Lock()
			candidate := session.result
			inflight := session.inflight
			failed := session.settlementFailed
			k.mu.Unlock()
			if failed {
				return nil, errModelUnknown
			}
			if candidate != nil && inflight == 0 {
				return candidate, nil
			}
			body, err := k.kubectl(ctx, nil, "get", "job", name, "-o", "json")
			if err != nil {
				return nil, err
			}
			var state struct {
				Status struct{ Failed, Succeeded int } `json:"status"`
			}
			if json.Unmarshal(body, &state) != nil {
				return nil, errors.New("invalid job state")
			}
			if state.Status.Failed > 0 || state.Status.Succeeded > 0 {
				return nil, errors.New("executor finished without accepted artifact")
			}
		}
	}
}

// ServeHTTP 只服务当前 E Attempt；短期 grant 不能变成通用模型代理或材料读取凭据。
func (k *KubernetesExecutor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 3 || parts[0] != "attempts" {
		http.NotFound(w, r)
		return
	}
	k.mu.Lock()
	session := k.sessions[parts[1]]
	k.mu.Unlock()
	if session == nil || r.Header.Get("Authorization") != "Bearer "+session.grant {
		http.Error(w, "forbidden", 403)
		return
	}
	a := session.assignment
	operation := "model"
	resource := a.Executor.Model
	if parts[2] == "materials" && len(parts) == 4 {
		operation = "read"
		resource = parts[3]
	}
	if _, err := k.Client.CheckTaskAccess(r.Context(), &pb.CheckTaskAccessRequest{Attempt: a.Attempt, Grant: session.grant, Operation: operation, ResourceRef: resource}); err != nil {
		http.Error(w, "attempt unavailable", 409)
		return
	}
	if r.Method == "POST" && parts[2] == "activate" && len(parts) == 3 {
		k.mu.Lock()
		defer k.mu.Unlock()
		if session.activated {
			http.Error(w, "already activated", 409)
			return
		}
		session.activated = true
		w.WriteHeader(204)
		return
	}
	k.mu.Lock()
	active := session.activated
	k.mu.Unlock()
	if !active {
		http.Error(w, "activation required", 409)
		return
	}
	if operation == "read" && r.Method == "GET" {
		stream, err := k.Client.ReadMaterial(r.Context(), &pb.ReadMaterialRequest{Attempt: a.Attempt, MaterialId: resource})
		if err != nil {
			http.Error(w, "unavailable", 503)
			return
		}
		for {
			chunk, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				return
			}
			if _, err = w.Write(chunk.Chunk); err != nil {
				return
			}
		}
	}
	if r.Method == "POST" && parts[2] == "result" && len(parts) == 3 {
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "invalid result", 400)
			return
		}
		g := &pb.GeneratedWork{}
		if protojson.Unmarshal(data, g) != nil || g.Work == nil || g.Work.Id != "work_"+a.Attempt.AttemptId {
			http.Error(w, "invalid result", 400)
			return
		}
		k.mu.Lock()
		defer k.mu.Unlock()
		if session.result != nil {
			if !proto.Equal(g, session.result) {
				http.Error(w, "result conflict", 409)
				return
			}
		} else {
			session.result = g
		}
		w.WriteHeader(204)
		return
	}
	if r.Method == "POST" && parts[2] == "upload" && len(parts) == 3 {
		k.upload(w, r, session)
		return
	}
	if r.Method == "POST" && len(parts) == 5 && parts[2] == "v1" && parts[3] == "chat" && parts[4] == "completions" {
		k.completions(w, r, session)
		return
	}
	http.NotFound(w, r)
}

// completions 固定模型、采样参数与命令工具，先记账再返回结果；不转换成其他模型协议。
func (k *KubernetesExecutor) completions(w http.ResponseWriter, r *http.Request, session *executorSession) {
	k.mu.Lock()
	if session.result != nil || session.inflight != 0 || session.settlementFailed {
		k.mu.Unlock()
		http.Error(w, "attempt busy or complete", 409)
		return
	}
	session.inflight++
	k.mu.Unlock()
	defer func() { k.mu.Lock(); session.inflight--; k.mu.Unlock() }()
	var body completionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || decoder.Decode(new(any)) != io.EOF || body.Stream || len(body.Messages) == 0 {
		http.Error(w, "invalid completion request", 400)
		return
	}
	if body.Model != session.assignment.Executor.Model {
		http.Error(w, "model forbidden", 403)
		return
	}
	// 本地命令的观察只返回文本；拒绝远程图片/文件 URL 和供应商托管工具。
	for _, m := range body.Messages {
		if _, ok := m.Content.(string); !ok && m.Content != nil {
			http.Error(w, "text messages required", 400)
			return
		}
		if m.Role != "system" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			http.Error(w, "message role forbidden", 400)
			return
		}
	}
	if len(body.Tools) != 0 {
		actual, _ := json.Marshal(body.Tools)
		expected, _ := json.Marshal(executionTools())
		if !bytes.Equal(actual, expected) {
			http.Error(w, "tool forbidden", 403)
			return
		}
	}
	body.MaxTokens = 8192
	body.Temperature, body.TopP = nil, nil
	for key, value := range session.assignment.Executor.Parameters.GetFields() {
		n := value.GetNumberValue()
		if key == "temperature" {
			body.Temperature = &n
		}
		if key == "top_p" {
			body.TopP = &n
		}
	}
	data, _ := json.Marshal(body)
	ctx, cancel := context.WithDeadline(r.Context(), session.assignment.Deadline.AsTime())
	defer cancel()
	reservation, err := k.Client.ReserveModelCall(ctx, &pb.ReserveModelCallRequest{Attempt: session.assignment.Attempt, Grant: session.grant, Model: body.Model, RequestId: "request_" + digest(data)[:32], RequestDigest: digest(data)})
	if err != nil || !reservation.DispatchPermit {
		http.Error(w, "dispatch unavailable", 409)
		return
	}
	response, outcome, callErr := k.Provider.complete(ctx, data)
	settleCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	_, err = k.Client.SettleModelCall(settleCtx, &pb.SettleModelCallRequest{Attempt: session.assignment.Attempt, ReservationId: reservation.ReservationId, Outcome: outcome})
	stop()
	if err != nil || outcome == "unknown" {
		k.mu.Lock()
		session.settlementFailed = true
		k.mu.Unlock()
		http.Error(w, "model outcome unknown", 503)
		return
	}
	if callErr != nil {
		http.Error(w, "model rejected", 502)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

func (k *KubernetesExecutor) upload(w http.ResponseWriter, r *http.Request, session *executorSession) {
	var metadata struct {
		Filename  string `json:"filename"`
		MediaType string `json:"mediaType"`
		Bytes     int64  `json:"bytes"`
		SHA256    string `json:"sha256"`
	}
	if json.Unmarshal([]byte(r.Header.Get("X-Artifact-Metadata")), &metadata) != nil || metadata.Bytes < 0 || metadata.Bytes > 16<<20 {
		http.Error(w, "invalid artifact", 400)
		return
	}
	stream, err := k.Client.UploadArtifact(r.Context())
	if err != nil {
		http.Error(w, "unavailable", 503)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	buffer := make([]byte, 65536)
	first := true
	for {
		n, e := r.Body.Read(buffer)
		if n > 0 || first {
			frame := &pb.UploadArtifactRequest{Chunk: buffer[:n]}
			if first {
				frame.Attempt = session.assignment.Attempt
				frame.UploadId = "upload_" + digest([]byte(metadata.Filename + metadata.SHA256))[:32]
				frame.Filename = metadata.Filename
				frame.MediaType = metadata.MediaType
				frame.Bytes = metadata.Bytes
				frame.Sha256 = metadata.SHA256
				first = false
			}
			if err = stream.Send(frame); err != nil {
				http.Error(w, "upload failed", 409)
				return
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			http.Error(w, "upload failed", 400)
			return
		}
	}
	response, err := stream.CloseAndRecv()
	if err != nil {
		http.Error(w, "upload failed", 409)
		return
	}
	data, _ := protojson.Marshal(response.Asset)
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}
