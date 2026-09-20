//go:build integration

package challenge_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challengeworker"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestChallengeStrictThresholdAndInvalidRankings(t *testing.T) {
	for _, threshold := range []float64{2.0 / 3.0, .6} {
		t.Run(fmt.Sprint(threshold), func(t *testing.T) {
			l := newLab(t)
			list, _ := l.deps.ListChallengeWorks(t.Context(), nil)
			l.deps.works = append(list.Works, &content.Work{Id: "middle", Artifacts: []*content.Artifact{{Value: &content.Artifact_Text{Text: "中间水平"}}}})
			l.deps.counts = map[string]int64{"left": 9, "middle": 5, "right": 1}
			cfg := config()
			cfg.RankerFitThreshold = threshold
			run := l.start(cfg)
			must(t, l.services[0].Reconcile(t.Context()))
			a := l.claim(0)
			l.activate(a)
			good := rankings(a.GetRank(), false)
			for _, mutate := range []func(*pb.Rankings){func(r *pb.Rankings) { r.Items[0].OrderedWorkIds[1] = "left" }, func(r *pb.Rankings) { r.Items[0].Judgments[0].EvidenceRefs = []string{"comment_1"} }, func(r *pb.Rankings) { r.Items[0].Judgments = r.Items[0].Judgments[:1] }} {
				bad := proto.Clone(good).(*pb.Rankings)
				mutate(bad)
				q := &pb.CompleteWorkRequest{Attempt: a.Attempt, Result: &pb.CompleteWorkRequest_Rankings{Rankings: bad}}
				q.ResultDigest = challenge.ResultDigest(q)
				_, err := l.workers[0].CompleteWork(t.Context(), q)
				code(t, err, codes.InvalidArgument)
			}
			l.complete(a, &pb.CompleteWorkRequest{Result: &pb.CompleteWorkRequest_Rankings{Rankings: good}})
			current := l.get(run.Id)
			if current.RankerFit.GetScore() != 2.0/3.0 {
				t.Fatal("score must be independently computed")
			}
			must(t, l.services[1].Reconcile(t.Context()))
			next := l.claim(0)
			if threshold == 2.0/3.0 {
				if next.Kind != pb.WorkKind_WORK_KIND_TRAIN_RANKER || current.Stage != "optimizing_ranker" {
					t.Fatal("equal threshold unlocked P")
				}
			} else if next.Kind != pb.WorkKind_WORK_KIND_PACK_TASK {
				t.Fatal("qualified R did not unlock P")
			}
		})
	}
	t.Run("insufficient", func(t *testing.T) {
		l := newLab(t)
		l.deps.counts = map[string]int64{"left": 5, "right": 5}
		_, err := l.admins[0].StartRun(t.Context(), &pb.StartRunRequest{ActorAssertion: "admin", TaskId: "target", Configuration: config(), IdempotencyKey: "ties"})
		code(t, err, codes.FailedPrecondition)
		if l.claim(0) != nil {
			t.Fatal("insufficient data scheduled work")
		}
	})
}

// expireLease 仅在专用测试数据库注入时钟边界，同时更新关系列及持久化状态。
func (l *lab) expireLease(run string) {
	l.t.Helper()
	var data []byte
	must(l.t, l.db.QueryRow(l.t.Context(), "SELECT state FROM challenge.runs WHERE id=$1", run).Scan(&data))
	stored := &pb.StoredRun{}
	must(l.t, proto.Unmarshal(data, stored))
	stored.Assignment.LeaseExpiresAt = timestamppb.New(time.Now().Add(-time.Minute))
	data, err := proto.Marshal(stored)
	must(l.t, err)
	_, err = l.db.Exec(l.t.Context(), "UPDATE challenge.runs SET state=$2,lease_until=clock_timestamp()-interval '1 minute' WHERE id=$1", run, data)
	must(l.t, err)
}

func TestChallengeLeaseRecoveryAndFreshRestart(t *testing.T) {
	l := newLab(t)
	run := l.start(config())
	must(t, l.services[0].Reconcile(t.Context()))
	old := l.claim(0)
	l.expireLease(run.Id)
	must(t, l.services[1].Reconcile(t.Context()))
	next := l.claim(0)
	if old.WorkItemId != next.WorkItemId || old.Attempt.AttemptId == next.Attempt.AttemptId || next.Attempt.LeaseEpoch <= old.Attempt.LeaseEpoch {
		t.Fatal("unactivated retry did not advance attempt fencing")
	}
	_, err := l.workers[0].RenewLease(t.Context(), &pb.RenewLeaseRequest{Attempt: old.Attempt})
	code(t, err, codes.FailedPrecondition)
	l.activate(next)
	l.expireLease(run.Id)
	must(t, l.services[0].Reconcile(t.Context()))
	must(t, l.services[1].Reconcile(t.Context()))
	if l.get(run.Id).Status != "failed" || l.claim(0) != nil {
		t.Fatal("activated attempt was blindly executed again")
	}
	restarted, err := l.admins[1].RestartRun(t.Context(), &pb.RestartRunRequest{ActorAssertion: "admin", ParentRunId: run.Id, Configuration: config(), IdempotencyKey: "restart"})
	must(t, err)
	if restarted.Run.ParentRunId != run.Id || restarted.Run.Id == run.Id || restarted.Run.Round != 0 || restarted.Run.RankerFit.Status == "passed" {
		t.Fatal("restart inherited optimization state")
	}
	must(t, l.services[1].Reconcile(t.Context()))
	if l.claim(0).Kind != pb.WorkKind_WORK_KIND_VALIDATE_RANKER {
		t.Fatal("restart skipped R validation")
	}
	queued := l.start(config())
	_, err = l.admins[0].CancelRun(t.Context(), &pb.CancelRunRequest{ActorAssertion: "admin", RunId: queued.Id, Reason: "测试启动前取消"})
	must(t, err)
	must(t, l.services[0].Reconcile(t.Context()))
	if l.get(queued.Id).Status != "cancelled" || l.deps.channels[queued.Id] != "closed" {
		t.Fatal("cancel before open lost tombstone")
	}
}

func TestChallengeMaterialAndUploadBoundaries(t *testing.T) {
	l := newLab(t)
	l.deps.data = []byte("任务附件")
	l.deps.asset = &asset.Asset{Id: "task_asset", Filename: "task.txt", MediaType: "text/plain", Bytes: int64(len(l.deps.data)), Sha256: digest(l.deps.data)}
	cfg := config()
	cfg.InitialTaskPackage.TaskAttachmentIds = []string{"task_asset"}
	l.start(cfg)
	must(t, l.services[0].Reconcile(t.Context()))
	a := l.claim(0)
	l.activate(a)
	stream, err := l.workers[0].ReadMaterial(t.Context(), &pb.ReadMaterialRequest{Attempt: a.Attempt, MaterialId: a.Materials[0].Id})
	must(t, err)
	chunk, err := stream.Recv()
	must(t, err)
	if string(chunk.Chunk) != "任务附件" || l.deps.lastReadTask != "validation_0" {
		t.Fatal("R attachment used target task authorization")
	}
	upload := func(a *pb.Assignment, name, checksum string) (*pb.UploadArtifactResponse, error) {
		stream, err := l.workers[0].UploadArtifact(t.Context())
		if err != nil {
			return nil, err
		}
		err = stream.Send(&pb.UploadArtifactRequest{Attempt: a.Attempt, UploadId: "upload_once", Filename: name, MediaType: "text/plain", Bytes: 6, Sha256: checksum, Chunk: []byte("作品")})
		if err != nil {
			return nil, err
		}
		return stream.CloseAndRecv()
	}
	_, err = upload(a, "work.txt", digest([]byte("作品")))
	code(t, err, codes.PermissionDenied)
	l.complete(a, &pb.CompleteWorkRequest{Result: &pb.CompleteWorkRequest_Rankings{Rankings: rankings(a.GetRank(), false)}})
	p := l.claim(0)
	l.activate(p)
	l.complete(p, &pb.CompleteWorkRequest{Result: &pb.CompleteWorkRequest_Prompt{Prompt: &pb.PromptResult{Prompt: "按照任务执行"}}})
	e := l.claim(0)
	l.activate(e)
	for _, name := range []string{"../work.txt", "report.md", "trace.jsonl"} {
		_, err = upload(e, name, digest([]byte("作品")))
		code(t, err, codes.InvalidArgument)
	}
	_, err = upload(e, "work.txt", strings.Repeat("0", 64))
	code(t, err, codes.InvalidArgument)
	stored, err := upload(e, "work.txt", digest([]byte("作品")))
	must(t, err)
	if stored.Asset.AttemptId != e.Attempt.AttemptId {
		t.Fatal("upload lost owner")
	}
	forged := proto.Clone(stored.Asset).(*asset.Asset)
	forged.AttemptId = "other_attempt"
	q := &pb.CompleteWorkRequest{Attempt: e.Attempt, Result: &pb.CompleteWorkRequest_Generated{Generated: &pb.GeneratedWork{Work: &content.Work{Id: "work_" + e.Attempt.AttemptId, Artifacts: []*content.Artifact{{Value: &content.Artifact_File{File: forged}}}}, Report: "执行完成"}}}
	q.ResultDigest = challenge.ResultDigest(q)
	_, err = l.workers[0].CompleteWork(t.Context(), q)
	code(t, err, codes.PermissionDenied)
}

func TestChallengeHTTPAuthorization(t *testing.T) {
	l := newLab(t)
	for _, item := range []struct{ method, path string }{{"GET", "/api/admin/challenge-runs"}, {"GET", "/api/admin/challenge-runs/run_none"}, {"POST", "/api/admin/challenge-runs"}, {"POST", "/api/admin/challenge-runs/run_none/cancel"}, {"POST", "/api/admin/challenge-runs/run_none/restart"}, {"PUT", "/api/admin/challenge-runs/run_none/candidates/candidate_none/registration"}} {
		for _, actor := range []string{"", "user", "mcp", "admin"} {
			if actor == "admin" && item.method == "GET" {
				continue
			}
			request, _ := http.NewRequest(item.method, l.web.URL+item.path, bytes.NewBufferString(`{}`))
			if actor == "mcp" {
				request.Header.Set("Authorization", "Bearer denied-mcp")
			} else if actor != "" {
				request.AddCookie(&http.Cookie{Name: gateway.SessionCookie, Value: actor})
			}
			response, err := l.web.Client().Do(request)
			must(t, err)
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 401 && response.StatusCode != 403 {
				t.Fatalf("%s %s %s: %d", actor, item.method, item.path, response.StatusCode)
			}
		}
	}
}

func TestChallengeHTTPRegistrationReceipt(t *testing.T) {
	l := newLab(t)
	cfg := config()
	cfg.Ranker.Prompt = "直接按准确性排序"
	cfg.RoundLimit = 1
	run := l.start(cfg)
	w := &challengeworker.Worker{Client: l.workers[0], Provider: l.modelProvider(), Executor: fixtureExecutor{l.deps}, Instance: "worker_0"}
	for step := 0; step < 12 && l.get(run.Id).Stage != "registering"; step++ {
		must(t, l.services[0].Reconcile(t.Context()))
		_, err := w.RunOnce(t.Context())
		must(t, err)
	}
	run = l.get(run.Id)
	if run.Stage != "registering" {
		t.Fatal("did not reach registration")
	}
	for _, want := range []int{201, 200} {
		q, _ := http.NewRequest("PUT", l.web.URL+"/api/admin/challenge-runs/"+run.Id+"/candidates/"+run.Candidates[0].Id+"/registration", nil)
		q.AddCookie(&http.Cookie{Name: gateway.SessionCookie, Value: "admin"})
		q.Header.Set("Origin", l.web.URL)
		q.Header.Set("X-CSRF-Token", "csrf")
		response, err := l.web.Client().Do(q)
		must(t, err)
		var receipt map[string]any
		err = json.NewDecoder(response.Body).Decode(&receipt)
		response.Body.Close()
		must(t, err)
		if response.StatusCode != want || receipt["created"] != (want == 201) || receipt["reviewState"] != "pending_review" || receipt["entryId"] == "" {
			t.Fatalf("invalid receipt: %d %+v", response.StatusCode, receipt)
		}
	}
	must(t, l.services[0].Reconcile(t.Context()))
	must(t, l.services[1].Reconcile(t.Context()))
	if l.get(run.Id).Status != "completed" || l.deps.registrations != 1 {
		t.Fatal("registration did not reconcile")
	}
}
