//go:build integration

package identity

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	challengepb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	discoverypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/discovery/v1"
	moderationpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/moderation/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	challengeproto "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/protobuf"
	challengerpc "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/adapter/rpc"
	challengeapp "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/application"
	challengedomain "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/domain"
	challengestore "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/repo/postgres"
	challengegrpc "github.com/KDZZZZZZ/human-worth/backend/internal/challenge/transport/grpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/discovery"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/repo/postgres"
	identitygrpc "github.com/KDZZZZZZ/human-worth/backend/internal/identity/transport/grpc"
	"github.com/KDZZZZZZ/human-worth/backend/internal/moderation"
	"github.com/KDZZZZZZ/human-worth/backend/internal/voting"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type backendLab struct {
	*additionalEntryLab
	cloud           contentpb.ContentServiceClient
	voting          [2]votingpb.VotingServiceClient
	votingContent   votingpb.VotingServiceClient
	votingChallenge [2]votingpb.VotingServiceClient
	challenges      [2]*challengeapp.Service
	workers         [2]challengepb.ChallengeServiceClient
}

// Independent replicas, real restricted PostgreSQL roles, files, Identity,
// Content, Moderation, Voting and Discovery. No fake business dependencies.
func newBackendLab(t *testing.T, additionalModels ...string) *backendLab {
	models := map[string]bool{"fixture-model": true}
	for _, model := range additionalModels {
		models[model] = true
	}
	l := newIdentityLab(t)
	f := &backendLab{additionalEntryLab: &additionalEntryLab{identity: l}}
	adb := serviceTestDatabase(t, l, "asset", asset.Migrate, "../../../ops/asset-grants.sql")
	f.contentDB = serviceTestDatabase(t, l, "content", content.Migrate, "../../../ops/content/grants.sql")
	f.reviewDB = serviceTestDatabase(t, l, "moderation", moderation.Migrate, "../../../ops/moderation/grants.sql")
	vdb := serviceTestDatabase(t, l, "voting", voting.Migrate, "../../../ops/voting/grants.sql")
	chdb := serviceTestDatabase(t, l, "challenge", challengestore.Migrate, "../../../ops/challenge/grants.sql")
	files := asset.FileStorage{Root: t.TempDir()}
	for i := range f.contents {
		f.contents[i] = &content.Server{DB: f.contentDB, Identity: l.client(t, i, "content", l.pki)}
	}
	cc := func(i int, caller string) contentpb.ContentServiceClient {
		return contentpb.NewContentServiceClient(serviceTestGRPC(t, l, "content", caller, content.Authorization, func(g *grpc.Server) { contentpb.RegisterContentServiceServer(g, f.contents[i]) }))
	}
	f.cloud = cc(0, "challenge")
	for i := range f.contents {
		vs := &voting.Server{DB: vdb, Identity: l.client(t, i, "voting", l.pki), Content: cc(i, "voting")}
		vc := func(caller string) votingpb.VotingServiceClient {
			return votingpb.NewVotingServiceClient(serviceTestGRPC(t, l, "voting", caller, voting.Authorization, func(g *grpc.Server) { votingpb.RegisterVotingServiceServer(g, vs) }))
		}
		f.voting[i] = vc("gateway")
		f.contents[i].Voting = vc("content")
		f.votingContent = f.contents[i].Voting
		f.votingChallenge[i] = vc("challenge")
		as := layeredAssetServer(adb, files, l.client(t, i, "asset", l.pki), cc(i, "asset"))
		ac := func(caller string) assetpb.AssetServiceClient {
			return assetpb.NewAssetServiceClient(serviceTestGRPC(t, l, "asset", caller, asset.Authorization, func(g *grpc.Server) { assetpb.RegisterAssetServiceServer(g, as) }))
		}
		f.contents[i].Asset = ac("content")
		f.gateways[i] = cc(i, "gateway")
		f.reviewers[i] = cc(i, "moderation")
		f.moderations[i] = &moderation.Server{DB: f.reviewDB, Identity: l.client(t, i, "moderation", l.pki), Content: f.reviewers[i]}
		mc := func(caller string) moderationpb.ModerationServiceClient {
			return moderationpb.NewModerationServiceClient(serviceTestGRPC(t, l, "moderation", caller, moderation.Authorization, func(g *grpc.Server) { moderationpb.RegisterModerationServiceServer(g, f.moderations[i]) }))
		}
		f.senders[i] = mc("content")
		store := challengestore.New(chdb)
		depsChallenge := challengerpc.Clients{Identity: l.client(t, i, "challenge", l.pki), Content: cc(i, "challenge"), Asset: ac("challenge"), Voting: vc("challenge")}
		var e error
		f.challenges[i], e = challengeapp.NewService(challengedomain.Config{Models: models, Harnesses: map[string]bool{"completion-shell": true}}, challengeapp.Dependencies{Reader: store, Transactions: store, Administrator: depsChallenge, Content: depsChallenge, Asset: depsChallenge, Voting: depsChallenge, Fingerprints: challengeproto.Fingerprints{}})
		must(t, e)
		chc := func(caller string) challengepb.ChallengeServiceClient {
			return challengepb.NewChallengeServiceClient(serviceTestGRPC(t, l, "challenge", caller, challengegrpc.Authorization, func(g *grpc.Server) {
				challengepb.RegisterChallengeServiceServer(g, challengegrpc.NewServer(f.challenges[i]))
			}))
		}
		f.workers[i] = chc("challenge-worker")
		ds := &discovery.Server{Identity: l.client(t, i, "discovery", l.pki), Content: cc(i, "discovery"), Voting: vc("discovery")}
		dc := discoverypb.NewDiscoveryServiceClient(serviceTestGRPC(t, l, "discovery", "gateway", discovery.Authorization, func(g *grpc.Server) { discoverypb.RegisterDiscoveryServiceServer(g, ds) }))
		h, e := gateway.New(l.clients[i], gateway.Options{Origin: l.origin, Content: f.gateways[i], Asset: ac("gateway"), Moderation: mc("gateway"), Voting: f.voting[i], Discovery: dc, Challenge: chc("gateway"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		must(t, e)
		l.web[i].Config.Handler = h
	}
	login := func(subject string) additionalActor {
		c, me := l.login(t, subject, nil)
		return additionalActor{c, me.Account.Id, me.CsrfToken}
	}
	f.alice, f.bob = login("backend-alice"), login("backend-bob")
	admin := login("backend-admin")
	must(t, identitygrpc.Error(application.NewAdmin(postgres.New(l.db), randomToken).ChangeAccount(t.Context(), "test-operator", dto.ChangeAccountInput{AccountID: admin.account, Role: "admin", State: "active", ExpectedVersion: 1})))
	f.admin = login("backend-admin")
	return f
}
func backendWork(side, title string) string {
	body := `{"side":%q,"title":%q,"source":"self","artifacts":[{"kind":"text","text":"complete original work"}],"permissions":{"cloudUse":true}`
	v := fmt.Sprintf(body, side, title)
	if side == "agent" {
		v += `,"agentConfiguration":{"model":"test-model","prompt":"private-config"}}`
	} else {
		v += "}"
	}
	return v
}
func (f *backendLab) publishTask(t *testing.T, key, attachment string) draftResult {
	attach := ""
	if attachment != "" {
		attach = fmt.Sprintf(`,"taskAttachments":[{"assetId":%q,"cloudUseAllowed":true}]`, attachment)
	}
	body := `{"title":"backend complete flow","summary":"compare","description":"Original task instructions","category":"writing","entries":[` + backendWork("human", "human") + "," + backendWork("agent", "agent") + "]" + attach + "}"
	var task draftResult
	readJSON(t, f.request(t, 0, f.alice, "POST", "/api/tasks", body, map[string]string{"Idempotency-Key": key}, 201), &task)
	readJSON(t, f.request(t, 1, f.alice, "POST", "/api/me/tasks/"+task.ID+"/review-requests", `{"expectedRevision":1}`, nil, 200), &task)
	_, e := f.contents[0].DeliverReview(t.Context(), f.senders[1])
	must(t, e)
	if attachment != "" {
		f.request(t, 1, f.admin, "GET", "/api/admin/reviews/"+*task.ReviewID+"/assets/"+attachment+"/content", "", nil, 200).Body.Close()
	}
	f.decide(t, *task.ReviewID, "approve", "complete works")
	must(t, f.contents[0].BootstrapCatalogs(t.Context()))
	return task
}

type flowBallot struct {
	ID, TaskID      string
	CatalogRevision int64
	Entries         []struct{ ID, Side string }
	CurrentVote     any
}

func (f *backendLab) ballot(t *testing.T, actor additionalActor, task string) flowBallot {
	var b flowBallot
	readJSON(t, f.request(t, 1, actor, "GET", "/api/tasks/"+task+"/ballot", "", nil, 200), &b)
	return b
}
func (f *backendLab) mcp(t *testing.T, token, tool string, args any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args, "_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}}})
	r := f.identity.request(t, 1, "POST", "/api/mcp", nil, string(body), map[string]string{"Authorization": "Bearer " + token, "Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": tool})
	httpStatus(t, r, 200)
	var out struct{ Result map[string]any }
	readJSON(t, r, &out)
	return out.Result
}
func flowJSON(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	var v map[string]any
	readJSON(t, r, &v)
	return v
}

func TestBackendPublicVotingModerationMCPFlow(t *testing.T) {
	f := newBackendLab(t)
	attachment := f.upload(t, f.alice, "backend-attachment-0001", []byte("independent task attachment"), true)
	task := f.publishTask(t, "backend-task-0001", attachment)
	other := f.publishTask(t, "backend-task-0002", "")
	page := flowJSON(t, f.request(t, 0, additionalActor{}, "GET", "/api/tasks?limit=1&q=backend", "", nil, 200))
	if len(page["items"].([]any)) != 1 || page["nextCursor"] == nil {
		t.Fatal("search pagination missing", page)
	}
	cursor := url.QueryEscape(page["nextCursor"].(string))
	next := flowJSON(t, f.request(t, 1, additionalActor{}, "GET", "/api/tasks?limit=1&q=backend&cursor="+cursor, "", nil, 200))
	if len(next["items"].([]any)) != 1 || page["items"].([]any)[0].(map[string]any)["id"] == next["items"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("cursor duplicated a task")
	}
	f.request(t, 0, additionalActor{}, "GET", "/api/tasks?q=other&cursor="+cursor, "", nil, 400).Body.Close()
	missing := flowJSON(t, f.request(t, 0, additionalActor{}, "GET", "/api/tasks?q=%25_no_literal_match", "", nil, 200))
	if len(missing["items"].([]any)) != 0 {
		t.Fatal("search treated SQL wildcard as text match")
	}
	taskPath := "/api/tasks/" + task.ID
	for _, path := range []string{"/api/tasks?limit=1", taskPath, taskPath + "/entries", "/api/board"} {
		v := flowJSON(t, f.request(t, 0, additionalActor{}, "GET", path, "", nil, 200))
		b, _ := json.Marshal(v)
		for _, secret := range []string{"totalVotes", "voteShare", "private-config", "agentConfiguration"} {
			if strings.Contains(string(b), secret) {
				t.Fatalf("ordinary read leaked %s", secret)
			}
		}
	}
	f.request(t, 0, additionalActor{}, "GET", taskPath+"/assets/"+attachment+"/content", "", nil, 200).Body.Close()
	b := f.ballot(t, f.alice, task.ID)
	if len(b.Entries) != 2 {
		t.Fatal("incomplete ballot")
	}
	f.request(t, 0, f.alice, "PUT", taskPath+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, b.ID, b.Entries[0].ID), nil, 200).Body.Close()
	f.request(t, 1, f.alice, "PUT", taskPath+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":1}`, b.ID, b.Entries[1].ID), nil, 200).Body.Close()
	var token struct{ Token string }
	readJSON(t, f.request(t, 0, f.alice, "POST", "/api/me/mcp-tokens", `{"name":"backend","createRequestId":"backend-mcp-one"}`, nil, 201), &token)
	for _, tool := range []string{"get_task", "list_entries", "list_comments"} {
		out := f.mcp(t, token.Token, tool, map[string]any{"taskId": task.ID})
		if out["isError"] != false {
			t.Fatalf("MCP %s: %v", tool, out)
		}
	}
	if v := flowJSON(t, f.request(t, 0, f.alice, "GET", taskPath+"/voting-eligibility", "", nil, 200)); v["canVote"] != true {
		t.Fatal("ordinary MCP read changed eligibility")
	}
	for _, tool := range []string{"cast_vote", "create_comment", "start_run"} {
		if f.mcp(t, token.Token, tool, map[string]any{})["isError"] != true {
			t.Fatal("MCP write tool exposed")
		}
	}
	if f.mcp(t, token.Token, "view_task_statistics", map[string]any{"taskId": task.ID})["isError"] != true {
		t.Fatal("implicit statistics read accepted")
	}
	out := f.mcp(t, token.Token, "view_task_statistics", map[string]any{"taskId": task.ID, "permanentlyCloseVoting": true})
	if out["isError"] != false {
		t.Fatal(out)
	}
	stats := out["structuredContent"].(map[string]any)
	if stats["totalVotes"] != float64(1) || stats["eligibility"].(map[string]any)["permanentlyClosed"] != true {
		t.Fatal("statistics did not commit closure", stats)
	}
	f.request(t, 1, f.alice, "GET", taskPath+"/ballot", "", nil, 403).Body.Close()
	f.ballot(t, f.alice, other.ID)
	readJSON(t, f.request(t, 1, f.alice, "POST", "/api/me/mcp-tokens", `{"name":"replacement","createRequestId":"backend-mcp-two"}`, nil, 201), &token)
	if f.mcp(t, token.Token, "view_task_statistics", map[string]any{"taskId": task.ID, "permanentlyCloseVoting": true})["isError"] != false {
		t.Fatal("token rotation broke stable identity")
	}
	comment := flowJSON(t, f.request(t, 1, f.alice, "POST", taskPath+"/comments", `{"body":"This work moved me."}`, map[string]string{"Idempotency-Key": "backend-comment-one"}, 201))
	replay := flowJSON(t, f.request(t, 0, f.alice, "POST", taskPath+"/comments", `{"body":"This work moved me."}`, map[string]string{"Idempotency-Key": "backend-comment-one"}, 201))
	if comment["id"] != replay["id"] {
		t.Fatal("duplicate comment")
	}
	f.request(t, 0, f.alice, "POST", taskPath+"/comments", `{"body":"different"}`, map[string]string{"Idempotency-Key": "backend-comment-one"}, 409).Body.Close()
	before := f.ballot(t, f.bob, task.ID)
	entry := f.submit(t, 1, f.alice, task.ID, "backend-additional-01", backendWork("human", "another human work"))
	f.deliver(t)
	f.decide(t, entry.ReviewID, "approve", "valid")
	// Completed publication invalidates ballots from the previous complete set.
	f.request(t, 0, f.bob, "PUT", taskPath+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, before.ID, before.Entries[0].ID), nil, 409).Body.Close()
	must(t, f.contents[1].BootstrapCatalogs(t.Context()))
	after := f.ballot(t, f.bob, task.ID)
	if len(after.Entries) != 3 {
		t.Fatal("new approved entry missing")
	}
	f.request(t, 1, f.bob, "PUT", taskPath+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, before.ID, before.Entries[0].ID), nil, 409).Body.Close()
	f.request(t, 0, f.alice, "GET", taskPath+"/ballot", "", nil, 403).Body.Close()
	reportBody := fmt.Sprintf(`{"targetKind":"comment","targetId":%q,"reason":"please review"}`, comment["id"])
	report := flowJSON(t, f.request(t, 0, f.bob, "POST", "/api/reports", reportBody, map[string]string{"Idempotency-Key": "backend-report-one"}, 201))
	f.request(t, 0, f.bob, "GET", "/api/admin/reports", "", nil, 403).Body.Close()
	f.request(t, 0, f.bob, "POST", "/api/admin/reports/"+report["id"].(string)+"/resolution", `{"action":"take_down","reason":"violation"}`, nil, 403).Body.Close()
	resolved := flowJSON(t, f.request(t, 1, f.admin, "POST", "/api/admin/reports/"+report["id"].(string)+"/resolution", `{"action":"take_down","reason":"violation"}`, nil, 200))
	if resolved["status"] != "actioned" {
		t.Fatal(resolved)
	}
	retry := flowJSON(t, f.request(t, 1, f.bob, "POST", "/api/reports", reportBody, map[string]string{"Idempotency-Key": "backend-report-one"}, 201))
	if retry["id"] != report["id"] || retry["status"] != "actioned" {
		t.Fatal("report retry lost its committed receipt")
	}
	comments := flowJSON(t, f.request(t, 0, additionalActor{}, "GET", taskPath+"/comments", "", nil, 200))
	if len(comments["items"].([]any)) != 0 {
		t.Fatal("taken-down comment visible")
	}
	must(t, f.contents[1].BootstrapCatalogs(t.Context()))
	for _, path := range []string{"/api/admin/dashboard", "/api/admin/audit-events?limit=1", "/api/admin/reports?status=actioned"} {
		f.request(t, 0, f.admin, "GET", path, "", nil, 200).Body.Close()
	}
	f.request(t, 0, f.admin, "POST", "/api/admin/takedowns", fmt.Sprintf(`{"targetKind":"task","targetId":%q,"reason":"task violation"}`, task.ID), map[string]string{"Idempotency-Key": "backend-takedown-task"}, 200).Body.Close()
	must(t, f.contents[0].BootstrapCatalogs(t.Context()))
	for _, suffix := range []string{"", "/entries", "/comments", "/assets/" + attachment + "/content"} {
		f.request(t, 1, additionalActor{}, "GET", taskPath+suffix, "", nil, 404).Body.Close()
	}
	f.request(t, 0, f.bob, "GET", taskPath+"/ballot", "", nil, 404).Body.Close()
	if f.mcp(t, token.Token, "get_task", map[string]any{"taskId": task.ID})["isError"] != true {
		t.Fatal("MCP exposed taken-down task")
	}
}

func TestBackendChallengeMaterialsRegistrationAndClosure(t *testing.T) {
	f := newBackendLab(t)
	task := f.publishTask(t, "backend-cloud-task", "")
	ref := &contentpb.SnapshotRef{TaskId: task.ID, CommentCutoff: timestamppb.Now()}
	desc, e := f.cloud.GetChallengeTaskDescription(t.Context(), &contentpb.GetChallengeTaskDescriptionRequest{Snapshot: ref})
	must(t, e)
	ref.TaskRevision = desc.Task.Revision
	works, e := f.cloud.ListChallengeWorks(t.Context(), &contentpb.ListChallengeWorksRequest{Snapshot: ref})
	must(t, e)
	ref.CatalogRevision = works.CatalogRevision
	if len(works.Works) != 2 || works.Works[0].Artifacts[0].GetText() == "" {
		t.Fatal("real work content missing")
	}
	for _, purpose := range []string{"WORK_KIND_EXECUTE_PACKAGE", "WORK_KIND_PACK_TASK"} {
		v, e := f.cloud.CheckChallengeMaterials(t.Context(), &contentpb.CheckChallengeMaterialsRequest{RunId: "run_backend_test_0001", Snapshot: ref, Purpose: purpose, WorkIds: []string{works.Works[0].Id}})
		must(t, e)
		if v.Allowed {
			t.Fatal("P/E got other works")
		}
	}
	v, e := f.cloud.CheckChallengeMaterials(t.Context(), &contentpb.CheckChallengeMaterialsRequest{RunId: "run_backend_test_0001", Snapshot: ref, Purpose: "WORK_KIND_RANK_WORKS", WorkIds: []string{works.Works[0].Id}})
	must(t, e)
	if !v.Allowed {
		t.Fatal("ranker denied authorized work")
	}
	run := "run_backend_register_01"
	_, e = f.cloud.OpenRunRegistration(t.Context(), &contentpb.OpenRunRegistrationRequest{RunId: run, TaskId: task.ID, AdministratorId: f.admin.account, ExecutorModel: "test-model"})
	must(t, e)
	candidate := &contentpb.RegisterChallengeCandidateRequest{RunId: run, TaskId: task.ID, CandidateId: "candidate_backend_0001", SourceAttemptId: "attempt_backend_00001", ExecutorConfigHash: strings.Repeat("a", 64), Work: &contentpb.Work{Id: "candidate_backend_0001", Artifacts: []*contentpb.Artifact{{Value: &contentpb.Artifact_Text{Text: "actual generated work"}}}}}
	results := make(chan *contentpb.RegisterChallengeCandidateResponse, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			r, e := f.cloud.RegisterChallengeCandidate(t.Context(), proto.Clone(candidate).(*contentpb.RegisterChallengeCandidateRequest))
			if e != nil {
				t.Error(e)
				return
			}
			results <- r
		})
	}
	wg.Wait()
	close(results)
	var id, review string
	created := 0
	for r := range results {
		if id != "" && id != r.Receipt.EntryId {
			t.Fatal("duplicate cloud entry")
		}
		id, review = r.Receipt.EntryId, r.Receipt.ReviewId
		if r.Receipt.Created {
			created++
		}
	}
	if id == "" || created != 1 {
		t.Fatal("cloud registration did not commit exactly once")
	}
	f.deliver(t)
	f.decide(t, review, "approve", "cloud result reviewed")
	must(t, f.contents[0].BootstrapCatalogs(t.Context()))
	if len(f.ballot(t, f.bob, task.ID).Entries) != 3 {
		t.Fatal("reviewed cloud work not published")
	}
	_, e = f.cloud.CloseRunRegistration(t.Context(), &contentpb.CloseRunRegistrationRequest{RunId: run, TaskId: task.ID})
	must(t, e)
	old, e := f.cloud.RegisterChallengeCandidate(t.Context(), candidate)
	must(t, e)
	if old.Receipt.EntryId != id || old.Receipt.ReviewState != "approved" {
		t.Fatal("receipt not recovered after closure")
	}
	late := proto.Clone(candidate).(*contentpb.RegisterChallengeCandidateRequest)
	late.CandidateId = "candidate_backend_late"
	late.Work.Id = late.CandidateId
	_, e = f.cloud.RegisterChallengeCandidate(t.Context(), late)
	grpcCode(t, e, codes.FailedPrecondition)
	reopen, e := f.cloud.OpenRunRegistration(t.Context(), &contentpb.OpenRunRegistrationRequest{RunId: run, TaskId: task.ID, AdministratorId: f.admin.account, ExecutorModel: "test-model"})
	must(t, e)
	if reopen.State != "closed" {
		t.Fatal("closed run reopened")
	}
	_, e = f.cloud.CloseRunRegistration(t.Context(), &contentpb.CloseRunRegistrationRequest{RunId: "run_closed_before_open", TaskId: task.ID})
	must(t, e)
	reopen, e = f.cloud.OpenRunRegistration(t.Context(), &contentpb.OpenRunRegistrationRequest{RunId: "run_closed_before_open", TaskId: task.ID, AdministratorId: f.admin.account, ExecutorModel: "test-model"})
	must(t, e)
	if reopen.State != "closed" {
		t.Fatal("late open bypassed tombstone")
	}
	// A revoked work invalidates a frozen authorization snapshot immediately.
	f.request(t, 0, f.admin, "POST", "/api/admin/takedowns", fmt.Sprintf(`{"targetKind":"entry","targetId":%q,"reason":"revoked"}`, works.Works[0].Id), nil, 200).Body.Close()
	_, e = f.cloud.ListChallengeWorks(t.Context(), &contentpb.ListChallengeWorksRequest{Snapshot: ref})
	grpcCode(t, e, codes.FailedPrecondition)
}

func TestBackendConclusionHistory(t *testing.T) {
	f := newBackendLab(t)
	task := f.publishTask(t, "conclusion-task-0001", "")
	actors := make([]additionalActor, 20)
	ballots := make([]flowBallot, 20)
	for i := range actors {
		cookie, me := f.identity.login(t, fmt.Sprintf("conclusion-voter-%d", i), nil)
		actors[i] = additionalActor{cookie, me.Account.Id, me.CsrfToken}
		ballots[i] = f.ballot(t, actors[i], task.ID)
	}
	cast := func(i int, side string, revision int) {
		b := ballots[i]
		entry := ""
		for _, e := range b.Entries {
			if e.Side == side {
				entry = e.ID
			}
		}
		f.request(t, i%2, actors[i], "PUT", "/api/tasks/"+task.ID+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":%d}`, b.ID, entry, revision), nil, 200).Body.Close()
	}
	board := func(human, agent float64) {
		v := flowJSON(t, f.request(t, 0, additionalActor{}, "GET", "/api/board", "", nil, 200))["overall"].(map[string]any)
		if v["humanLeadingTasks"] != human || v["agentLeadingTasks"] != agent {
			t.Fatal("incorrect conclusion", v)
		}
	}
	// Concurrent votes must serialize both current choice and conclusion history.
	var group sync.WaitGroup
	for i := range actors {
		group.Add(1)
		go func(i int) { defer group.Done(); cast(i, "human", 0) }(i)
	}
	group.Wait()
	board(1, 0)
	for i := 0; i < 10; i++ {
		cast(i, "agent", 1)
	}
	board(0, 0)
	for i := 10; i < 20; i++ {
		cast(i, "agent", 1)
	}
	board(0, 1)
	cast(19, "agent", 1) // A response replay cannot add another flip.
	stats := flowJSON(t, f.request(t, 1, f.admin, "POST", "/api/tasks/"+task.ID+"/statistics-access", `{"permanentlyCloseVoting":true}`, nil, 200))
	if stats["totalVotes"] != float64(20) || stats["flipCount"] != float64(1) || stats["lastFlipAt"] == nil {
		t.Fatal("persistent flip history mismatch", stats)
	}
	var flips int
	must(t, f.identity.db.QueryRow(t.Context(), `SELECT count(*) FROM voting.conclusion_events WHERE task_id=$1 AND is_flip`, task.ID).Scan(&flips))
	if flips != 1 {
		t.Fatal("duplicate conclusion history")
	}
}

func TestBackendMCPProtocolAndRevocation(t *testing.T) {
	f := newBackendLab(t)
	var credential struct {
		Token      string
		Credential struct{ ID string }
	}
	readJSON(t, f.request(t, 0, f.alice, "POST", "/api/me/mcp-tokens", `{"name":"protocol","createRequestId":"mcp-protocol-check"}`, nil, 201), &credential)
	call := func(body string, overrides map[string]string, code int) map[string]any {
		headers := map[string]string{"Authorization": "Bearer " + credential.Token, "Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
		for k, v := range overrides {
			headers[k] = v
		}
		r := f.identity.request(t, 1, "POST", "/api/mcp", nil, body, headers)
		httpStatus(t, r, code)
		if code == 202 {
			r.Body.Close()
			return nil
		}
		return flowJSON(t, r)
	}
	init := call(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"integration","version":"1"}}}`, nil, 200)
	if init["result"].(map[string]any)["protocolVersion"] != "2025-11-25" {
		t.Fatal(init)
	}
	call(`{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil, 202)
	call(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":12,"reason":"cancelled by client"}}`, nil, 202)
	tools := call(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, nil, 200)["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 5 {
		t.Fatal("unexpected MCP surface")
	}
	for _, v := range tools {
		tool := v.(map[string]any)
		if tool["name"] == "view_task_statistics" && (!strings.Contains(tool["description"].(string), "永久") || tool["annotations"].(map[string]any)["readOnlyHint"] != false) {
			t.Fatal("statistics side effect hidden")
		}
	}
	for _, body := range []string{`{`, `[]`, `{"jsonrpc":"2.0","id":null,"method":"ping"}`, `{"jsonrpc":"2.0","id":1.5,"method":"ping"}`} {
		call(body, nil, 400)
	}
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	call(ping, map[string]string{"Origin": "https://untrusted.example"}, 403)
	call(ping, map[string]string{"Accept": "application/json"}, 406)
	call(ping, map[string]string{"Cookie": "borrowed=session"}, 401)
	call(ping, map[string]string{"MCP-Protocol-Version": "2030-01-01"}, 400)
	meta := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`
	call(meta, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call"}, 400)
	discover := call(meta, map[string]string{"MCP-Protocol-Version": "2026-07-28", "Mcp-Method": "server/discover"}, 200)
	if discover["result"].(map[string]any)["resultType"] != "complete" {
		t.Fatal(discover)
	}
	f.request(t, 0, f.alice, "DELETE", "/api/me/mcp-tokens/"+credential.Credential.ID, "", nil, 204).Body.Close()
	call(ping, nil, 401)
}

// Uses real Content closure and fresh reads from the other Challenge replica.
func TestBackendChallengeCancelAndRestart(t *testing.T) {
	f := newBackendLab(t)
	target := f.publishTask(t, "cancel-target-task", "")
	for i := 0; i < 2; i++ {
		task := f.publishTask(t, fmt.Sprintf("cancel-sample-%02d", i), "")
		b := f.ballot(t, f.alice, task.ID)
		f.request(t, 0, f.alice, "PUT", "/api/tasks/"+task.ID+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, b.ID, b.Entries[0].ID), nil, 200).Body.Close()
	}
	config := `{"initialTaskPackage":{"taskRevision":1,"description":"Produce work","workRequirements":"complete"},"packer":{"model":"fixture-model"},"ranker":{"model":"fixture-model"},"executor":{"model":"fixture-model","harness":"completion-shell"},"rankerFitThreshold":0.8,"rankerMinComparablePairs":1,"rankerRoundLimit":1,"roundLimit":1,"budget":{"amount":30,"unit":"model_calls"}}`
	body := fmt.Sprintf(`{"taskId":%q,"configuration":%s}`, target.ID, config)
	f.request(t, 0, f.alice, "POST", "/api/admin/challenge-runs", body, map[string]string{"Idempotency-Key": "cancel-forbidden"}, 403).Body.Close()
	run := flowJSON(t, f.request(t, 0, f.admin, "POST", "/api/admin/challenge-runs", body, map[string]string{"Idempotency-Key": "cancel-original-run"}, 202))
	id := run["id"].(string)
	var claimed *challengepb.Assignment
	for i := 0; i < 10 && claimed == nil; i++ {
		must(t, f.challenges[i%2].Reconcile(t.Context()))
		v, e := f.workers[0].ClaimWork(t.Context(), &challengepb.ClaimWorkRequest{WorkerInstanceId: "cancel-worker", ClaimRequestId: fmt.Sprintf("cancel-claim-%d", i), Capabilities: []challengepb.WorkKind{1, 2, 3, 4, 5, 6, 7}})
		must(t, e)
		claimed = v.Assignment
	}
	if claimed == nil {
		t.Fatal("no real work claimed")
	}
	path := "/api/admin/challenge-runs/" + id
	f.request(t, 0, f.bob, "POST", path+"/cancel", `{"reason":"stop"}`, nil, 403).Body.Close()
	f.request(t, 1, f.admin, "POST", path+"/cancel", `{"reason":"stop"}`, nil, 202).Body.Close()
	for i := 0; i < 5; i++ {
		must(t, f.challenges[i%2].Reconcile(t.Context()))
	}
	run = flowJSON(t, f.request(t, 0, f.admin, "GET", path, "", nil, 200))
	if run["status"] != "cancelled" {
		t.Fatal(run)
	}
	var state string
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT state FROM content.run_registrations WHERE run_id=$1`, id).Scan(&state))
	if state != "closed" {
		t.Fatal("registration remains open")
	}
	late := &challengepb.CompleteWorkRequest{Attempt: claimed.Attempt, Result: &challengepb.CompleteWorkRequest_Prompt{Prompt: &challengepb.PromptResult{Prompt: "late criteria"}}}
	late.ResultDigest = challengeproto.ResultDigest(late)
	accepted, e := f.workers[0].CompleteWork(t.Context(), late)
	if e == nil && accepted.GetAccepted() {
		t.Fatal("terminated attempt advanced")
	}
	restarted := flowJSON(t, f.request(t, 0, f.admin, "POST", path+"/restart", `{"configuration":`+config+`}`, map[string]string{"Idempotency-Key": "cancel-restart-run"}, 202))
	if restarted["id"] == id || restarted["parentRunId"] != id {
		t.Fatal("restart lost lineage", restarted)
	}
	var entries int
	must(t, f.contentDB.QueryRow(t.Context(), `SELECT count(*) FROM content.challenge_registrations WHERE run_id=$1`, id).Scan(&entries))
	if entries != 0 {
		t.Fatal("cancelled run registered a work")
	}
}

func TestBackendPreferenceSnapshots(t *testing.T) {
	f := newBackendLab(t)
	for i := 0; i < 2; i++ {
		task := f.publishTask(t, fmt.Sprintf("preference-sample-%02d", i), "")
		b := f.ballot(t, f.alice, task.ID)
		f.request(t, 0, f.alice, "PUT", "/api/tasks/"+task.ID+"/vote", fmt.Sprintf(`{"ballotId":%q,"entryId":%q,"expectedVoteRevision":0}`, b.ID, b.Entries[0].ID), nil, 200).Body.Close()
	}
	request := &votingpb.GetHumanPreferenceSnapshotRequest{RunId: "run_supervision_000001", Purpose: "training"}
	var training [2]*votingpb.GetHumanPreferenceSnapshotResponse
	var failures [2]error
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			training[i], failures[i] = f.votingChallenge[i].GetHumanPreferenceSnapshot(t.Context(), request)
		}(i)
	}
	wg.Wait()
	for _, e := range failures {
		must(t, e)
	}
	if !proto.Equal(training[0], training[1]) || len(training[0].Samples) != 1 {
		t.Fatal("training replay changed immutable sample")
	}
	sample := training[0].Samples[0]
	if !sample.Snapshot.CommentCutoff.AsTime().Before(sample.WindowStart.AsTime()) || sample.ValidVoteCount != 1 {
		t.Fatal("window/labels invalid")
	}
	validation, e := f.votingChallenge[1].GetHumanPreferenceSnapshot(t.Context(), &votingpb.GetHumanPreferenceSnapshotRequest{RunId: request.RunId, Purpose: "validation", ExcludedTaskIds: []string{sample.Snapshot.TaskId}})
	must(t, e)
	if len(validation.Samples) != 1 || validation.Samples[0].Snapshot.TaskId == sample.Snapshot.TaskId {
		t.Fatal("validation reused training task")
	}
	_, e = f.votingChallenge[0].GetHumanPreferenceSnapshot(t.Context(), &votingpb.GetHumanPreferenceSnapshotRequest{RunId: request.RunId, Purpose: "validation", BatchIndex: 1})
	grpcCode(t, e, codes.FailedPrecondition)
	_, e = f.votingChallenge[0].GetHumanPreferenceSnapshot(t.Context(), &votingpb.GetHumanPreferenceSnapshotRequest{RunId: request.RunId, Purpose: "training", ExcludedTaskIds: []string{sample.Snapshot.TaskId}})
	grpcCode(t, e, codes.Aborted)
	check := &votingpb.CheckHumanPreferenceSnapshotRequest{SnapshotId: training[0].SnapshotId, PolicyVersion: training[0].PolicyVersion}
	v, e := f.votingChallenge[1].CheckHumanPreferenceSnapshot(t.Context(), check)
	must(t, e)
	if !v.Valid {
		t.Fatal("other replica could not recover snapshot")
	}
	_, e = f.voting[0].GetHumanPreferenceSnapshot(t.Context(), request)
	grpcCode(t, e, codes.PermissionDenied)
	f.request(t, 0, f.admin, "POST", "/api/admin/takedowns", fmt.Sprintf(`{"targetKind":"task","targetId":%q,"reason":"invalid sample"}`, sample.Snapshot.TaskId), map[string]string{"Idempotency-Key": "preference-takedown"}, 200).Body.Close()
	v, e = f.votingChallenge[0].CheckHumanPreferenceSnapshot(t.Context(), check)
	must(t, e)
	if v.Valid {
		t.Fatal("takedown did not invalidate frozen supervision")
	}
}

func TestBackendPendingCatalogDeniesNewPublicReads(t *testing.T) {
	f := newBackendLab(t)
	attachment := f.upload(t, f.alice, "pending-gate-attachment", []byte("private during pending change\n"), true)
	task := f.publishTask(t, "pending-gate-task", attachment)
	f.request(t, 0, f.alice, "POST", "/api/tasks/"+task.ID+"/comments", `{"body":"existing discussion"}`, nil, 201).Body.Close()
	// Persisted active intent is the same deny fence used by the real outbox.
	_, e := f.contentDB.Exec(t.Context(), `UPDATE content.voting_catalog_heads SET active_change='fault-pending-change' WHERE task_id=$1`, task.ID)
	must(t, e)
	for _, suffix := range []string{"", "/comments", "/entries", "/assets/" + attachment + "/content"} {
		f.request(t, 1, additionalActor{}, "GET", "/api/tasks/"+task.ID+suffix, "", nil, 404).Body.Close()
	}
	listing := flowJSON(t, f.request(t, 0, additionalActor{}, "GET", "/api/tasks", "", nil, 200))
	if len(listing["items"].([]any)) != 0 {
		t.Fatal("discovery leaked pending task")
	}
	_, e = f.cloud.GetChallengeTaskDescription(t.Context(), &contentpb.GetChallengeTaskDescriptionRequest{Snapshot: &contentpb.SnapshotRef{TaskId: task.ID}})
	grpcCode(t, e, codes.NotFound)
	_, e = f.contentDB.Exec(t.Context(), `UPDATE content.voting_catalog_heads SET active_change=NULL WHERE task_id=$1`, task.ID)
	must(t, e)
	f.request(t, 1, additionalActor{}, "GET", "/api/tasks/"+task.ID+"/assets/"+attachment+"/content", "", nil, 200).Body.Close()
}
