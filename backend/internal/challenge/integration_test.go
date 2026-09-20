//go:build integration

package challenge_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identity "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challengeworker"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const privateComment = "用户评论机密标记：不能向 P 或 E 转交这段具体评论内容"

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func code(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if status.Code(err) != want {
		t.Fatalf("got %v, want %v", err, want)
	}
}
func digest(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// dependencies 是带状态的真实 gRPC fake：关闭墓碑、登记幂等及授权撤销都有实际行为。
type dependencies struct {
	content.UnimplementedContentServiceServer
	asset.UnimplementedAssetServiceServer
	voting.UnimplementedVotingServiceServer
	identity.UnimplementedIdentityServiceServer
	mu                                    sync.Mutex
	channels                              map[string]string
	receipts                              map[string]*content.RegistrationReceipt
	allowed, preferenceValid              bool
	loseRegistration, closeUnavailable    bool
	registrations, modelCalls, executions int
	asset                                 *asset.Asset
	data                                  []byte
	requests                              []string
	works                                 []*content.Work
	counts                                map[string]int64
	uploads                               map[string]*asset.Asset
	lastReadTask                          string
}

func (d *dependencies) principal(raw string) (*identity.Principal, error) {
	switch raw {
	case "admin":
		return &identity.Principal{AccountId: "account_admin", Role: identity.Role_ROLE_ADMIN, ClientKind: identity.ClientKind_CLIENT_KIND_WEB_SESSION}, nil
	case "user":
		return &identity.Principal{AccountId: "account_user", Role: identity.Role_ROLE_USER, ClientKind: identity.ClientKind_CLIENT_KIND_WEB_SESSION}, nil
	case "mcp":
		return &identity.Principal{AccountId: "account_admin", Role: identity.Role_ROLE_ADMIN, ClientKind: identity.ClientKind_CLIENT_KIND_MCP_READ}, nil
	}
	return nil, status.Error(codes.Unauthenticated, "unauthenticated")
}
func (d *dependencies) VerifyActor(_ context.Context, q *identity.VerifyActorRequest) (*identity.VerifyActorResponse, error) {
	p, err := d.principal(q.ActorAssertion)
	return &identity.VerifyActorResponse{Principal: p}, err
}
func (d *dependencies) ResolvePrincipal(_ context.Context, q *identity.ResolvePrincipalRequest) (*identity.ResolvePrincipalResponse, error) {
	raw := q.GetSessionCookie()
	if q.GetMcpToken() != "" {
		raw = "mcp"
	}
	p, err := d.principal(raw)
	if err != nil {
		return nil, err
	}
	if q.Audience != "challenge" || p.Role != identity.Role_ROLE_ADMIN || p.ClientKind != identity.ClientKind_CLIENT_KIND_WEB_SESSION {
		return nil, status.Error(codes.PermissionDenied, "administrator_required")
	}
	read := strings.HasSuffix(q.FullMethod, "/GetRun") || strings.HasSuffix(q.FullMethod, "/ListRuns")
	if !read && (q.Origin == "" || q.CsrfToken != "csrf") {
		return nil, status.Error(codes.PermissionDenied, "csrf_invalid")
	}
	return &identity.ResolvePrincipalResponse{Principal: p, ActorAssertion: raw}, nil
}
func (d *dependencies) GetChallengeTaskDescription(_ context.Context, q *content.GetChallengeTaskDescriptionRequest) (*content.GetChallengeTaskDescriptionResponse, error) {
	if q.Snapshot.TaskRevision > 1 {
		return nil, status.Error(codes.FailedPrecondition, "task_changed")
	}
	task := &content.TaskDescription{TaskId: q.Snapshot.TaskId, Revision: 1, Description: "按准确性与清晰度比较作品。"}
	if d.asset != nil {
		task.Attachments = []*content.TaskAttachment{{Asset: d.asset, CloudUseAllowed: true}}
	}
	return &content.GetChallengeTaskDescriptionResponse{Task: task, Visible: true}, nil
}
func (d *dependencies) ListChallengeWorks(context.Context, *content.ListChallengeWorksRequest) (*content.ListChallengeWorksResponse, error) {
	if d.works != nil {
		return &content.ListChallengeWorksResponse{CatalogRevision: 1, Works: d.works}, nil
	}
	return &content.ListChallengeWorksResponse{CatalogRevision: 1, Works: []*content.Work{{Id: "left", Artifacts: []*content.Artifact{{Value: &content.Artifact_Text{Text: "准确完整的作品"}}}}, {Id: "right", Artifacts: []*content.Artifact{{Value: &content.Artifact_Text{Text: "错误不完整的作品"}}}}}}, nil
}
func (d *dependencies) ListChallengeComments(_ context.Context, q *content.ListChallengeCommentsRequest) (*content.ListChallengeCommentsResponse, error) {
	return &content.ListChallengeCommentsResponse{CutoffAt: q.Snapshot.CommentCutoff, Comments: []*content.Comment{{Id: "comment_1", Body: privateComment}}}, nil
}
func (d *dependencies) CheckChallengeMaterials(context.Context, *content.CheckChallengeMaterialsRequest) (*content.CheckChallengeMaterialsResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &content.CheckChallengeMaterialsResponse{Allowed: d.allowed, AuthorizationVersion: "1"}, nil
}
func (d *dependencies) OpenRunRegistration(_ context.Context, q *content.OpenRunRegistrationRequest) (*content.OpenRunRegistrationResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.channels[q.RunId] != "closed" {
		d.channels[q.RunId] = "open"
	}
	return &content.OpenRunRegistrationResponse{State: d.channels[q.RunId]}, nil
}
func (d *dependencies) CloseRunRegistration(_ context.Context, q *content.CloseRunRegistrationRequest) (*content.CloseRunRegistrationResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closeUnavailable {
		return nil, status.Error(codes.Unavailable, "injected_close_failure")
	}
	d.channels[q.RunId] = "closed"
	return &content.CloseRunRegistrationResponse{State: "closed"}, nil
}
func (d *dependencies) GetChallengeRegistration(_ context.Context, q *content.GetChallengeRegistrationRequest) (*content.GetChallengeRegistrationResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := d.receipts[q.RunId+"/"+q.CandidateId]
	if r == nil {
		return nil, status.Error(codes.NotFound, "registration_not_found")
	}
	out := proto.Clone(r).(*content.RegistrationReceipt)
	out.Created = false
	return &content.GetChallengeRegistrationResponse{Receipt: out}, nil
}
func (d *dependencies) RegisterChallengeCandidate(_ context.Context, q *content.RegisterChallengeCandidateRequest) (*content.RegisterChallengeCandidateResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	key := q.RunId + "/" + q.CandidateId
	if existing := d.receipts[key]; existing != nil {
		return &content.RegisterChallengeCandidateResponse{Receipt: proto.Clone(existing).(*content.RegistrationReceipt)}, nil
	}
	if d.channels[q.RunId] != "open" {
		return nil, status.Error(codes.FailedPrecondition, "registration_closed")
	}
	r := &content.RegistrationReceipt{RunId: q.RunId, CandidateId: q.CandidateId, EntryId: "entry_" + q.CandidateId, ReviewState: "pending_review", Created: true}
	d.receipts[key] = r
	d.registrations++
	if d.loseRegistration {
		d.loseRegistration = false
		return nil, status.Error(codes.Unavailable, "response_lost")
	}
	return &content.RegisterChallengeCandidateResponse{Receipt: proto.Clone(r).(*content.RegistrationReceipt)}, nil
}
func (d *dependencies) GetHumanPreferenceSnapshot(_ context.Context, q *voting.GetHumanPreferenceSnapshotRequest) (*voting.GetHumanPreferenceSnapshotResponse, error) {
	task := fmt.Sprintf("%s_%d", q.Purpose, q.BatchIndex)
	for _, old := range q.ExcludedTaskIds {
		if old == task {
			return nil, status.Error(codes.FailedPrecondition, "independent_samples_exhausted")
		}
	}
	now := time.Now()
	ref := &content.SnapshotRef{TaskId: task, TaskRevision: 1, CatalogRevision: 1, CommentCutoff: timestamppb.New(now.Add(-3 * time.Hour))}
	counts := d.counts
	if counts == nil {
		counts = map[string]int64{"left": 9, "right": 1}
	}
	var total int64
	for _, n := range counts {
		total += n
	}
	works, _ := d.ListChallengeWorks(context.Background(), nil)
	return &voting.GetHumanPreferenceSnapshotResponse{SnapshotId: q.RunId + "_" + task, PolicyVersion: "fixture_v1", ValidUntil: timestamppb.New(now.Add(time.Hour)), Samples: []*voting.HumanPreference{{Snapshot: ref, WorkSetHash: challenge.WorkSetHash(works.Works), WindowStart: timestamppb.New(now.Add(-2 * time.Hour)), WindowEnd: timestamppb.New(now.Add(-time.Hour)), CountsByWork: counts, ValidVoteCount: total}}}, nil
}
func (d *dependencies) CheckHumanPreferenceSnapshot(context.Context, *voting.CheckHumanPreferenceSnapshotRequest) (*voting.CheckHumanPreferenceSnapshotResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &voting.CheckHumanPreferenceSnapshotResponse{Valid: d.preferenceValid}, nil
}
func (d *dependencies) GetAsset(_ context.Context, q *asset.GetAssetRequest) (*asset.GetAssetResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if stored := d.uploads[q.AssetId]; stored != nil {
		return &asset.GetAssetResponse{Asset: stored}, nil
	}
	if d.asset == nil || q.AssetId != d.asset.Id {
		return nil, status.Error(codes.NotFound, "asset_not_found")
	}
	return &asset.GetAssetResponse{Asset: d.asset}, nil
}
func (d *dependencies) ReadAsset(q *asset.ReadAssetRequest, stream grpc.ServerStreamingServer[asset.ReadAssetResponse]) error {
	d.mu.Lock()
	d.lastReadTask = q.Access.TaskId
	d.mu.Unlock()
	if d.asset == nil || q.AssetId != d.asset.Id {
		return status.Error(codes.NotFound, "asset_not_found")
	}
	for offset := q.Offset; offset < int64(len(d.data)); {
		end := min(offset+65536, int64(len(d.data)))
		if err := stream.Send(&asset.ReadAssetResponse{Offset: offset, Chunk: d.data[offset:end], Sha256: d.asset.Sha256}); err != nil {
			return err
		}
		offset = end
	}
	return nil
}

func (d *dependencies) UploadAsset(stream grpc.ClientStreamingServer[asset.UploadAssetRequest, asset.UploadAssetResponse]) error {
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	var data []byte
	frame := first
	for {
		data = append(data, frame.Chunk...)
		frame, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if int64(len(data)) != first.Bytes || digest(data) != first.Sha256 {
		return status.Error(codes.InvalidArgument, "invalid upload")
	}
	a := &asset.Asset{Id: "asset_" + first.UploadId, Filename: first.Filename, MediaType: first.MediaType, Bytes: first.Bytes, Sha256: first.Sha256, RunId: first.Access.RunId, AttemptId: first.Access.AttemptId}
	d.mu.Lock()
	if d.uploads == nil {
		d.uploads = map[string]*asset.Asset{}
	}
	if old := d.uploads[a.Id]; old != nil {
		a = old
	} else {
		d.uploads[a.Id] = a
	}
	d.mu.Unlock()
	return stream.SendAndClose(&asset.UploadAssetResponse{Asset: a})
}

type pki struct {
	t       *testing.T
	root    *x509.Certificate
	key     *ecdsa.PrivateKey
	ca, dir string
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(t, err)
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "challenge test CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, root, root, &key.PublicKey, key)
	must(t, err)
	cert, err := x509.ParseCertificate(der)
	must(t, err)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.crt")
	must(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	return &pki{t: t, root: cert, key: key, ca: ca, dir: dir}
}
func (p *pki) cert(service string) (string, string) {
	p.t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(p.t, err)
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	must(p.t, err)
	uri, _ := url.Parse(platform.ServiceURIPrefix + service)
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: service}, DNSNames: []string{service}, URIs: []*url.URL{uri}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, p.root, &key.PublicKey, p.key)
	must(p.t, err)
	raw, err := x509.MarshalECPrivateKey(key)
	must(p.t, err)
	certPath := filepath.Join(p.dir, serial.String()+".crt")
	keyPath := filepath.Join(p.dir, serial.String()+".key")
	must(p.t, os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600))
	must(p.t, os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: raw}), 0600))
	return certPath, keyPath
}
func (p *pki) serve(service string, register func(*grpc.Server), authorize bool) string {
	cert, key := p.cert(service)
	tls, err := platform.TLS(cert, key, p.ca, "")
	must(p.t, err)
	opts := []grpc.ServerOption{grpc.Creds(credentials.NewTLS(tls))}
	if authorize {
		opts = append(opts, grpc.UnaryInterceptor(challenge.Authorization), grpc.StreamInterceptor(challenge.AuthorizationStream))
	}
	s := grpc.NewServer(opts...)
	register(s)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	must(p.t, err)
	go s.Serve(l)
	p.t.Cleanup(s.Stop)
	return l.Addr().String()
}
func (p *pki) dial(address, service, caller string) *grpc.ClientConn {
	cert, key := p.cert(caller)
	tls, err := platform.TLS(cert, key, p.ca, service)
	must(p.t, err)
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(tls)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2<<20), grpc.MaxCallSendMsgSize(2<<20)))
	must(p.t, err)
	p.t.Cleanup(func() { conn.Close() })
	return conn
}

type lab struct {
	t        *testing.T
	db       *pgxpool.Pool
	services [2]*challenge.Server
	admins   [2]pb.ChallengeServiceClient
	workers  [2]pb.ChallengeServiceClient
	deps     *dependencies
	pki      *pki
	web      *httptest.Server
	identity identity.IdentityServiceClient
}

func newLab(t *testing.T) *lab {
	t.Helper()
	dsn := os.Getenv("CHALLENGE_TEST_DATABASE_URL")
	if path := os.Getenv("CHALLENGE_TEST_DATABASE_URL_FILE"); path != "" {
		raw, err := os.ReadFile(path)
		must(t, err)
		dsn = strings.TrimSpace(string(raw))
	}
	if dsn == "" {
		t.Fatal("integration requires CHALLENGE_TEST_DATABASE_URL[_FILE] pointing to an isolated test server")
	}
	admin, err := pgxpool.New(t.Context(), dsn)
	must(t, err)
	name := fmt.Sprintf("hw_challenge_%d", time.Now().UnixNano())
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{name}.Sanitize())
	must(t, err)
	cfg, err := pgxpool.ParseConfig(dsn)
	must(t, err)
	cfg.ConnConfig.Database = name
	db, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	var runtimeDB, ownerDB *pgxpool.Pool
	roles := []string{name + "_owner", name + "_runtime"}
	t.Cleanup(func() {
		if runtimeDB != nil {
			runtimeDB.Close()
		}
		if ownerDB != nil {
			ownerDB.Close()
		}
		db.Close()
		admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		for _, role := range roles {
			admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{role}.Sanitize())
		}
		admin.Close()
	})
	passwordBytes := make([]byte, 24)
	_, err = rand.Read(passwordBytes)
	must(t, err)
	password := hex.EncodeToString(passwordBytes)
	for _, role := range roles {
		_, err = db.Exec(t.Context(), "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" LOGIN PASSWORD '"+password+"'")
		must(t, err)
	}
	_, err = db.Exec(t.Context(), "CREATE SCHEMA challenge AUTHORIZATION "+pgx.Identifier{roles[0]}.Sanitize()+"; REVOKE CREATE ON DATABASE "+pgx.Identifier{name}.Sanitize()+" FROM PUBLIC; CREATE SCHEMA unrelated; CREATE TABLE unrelated.private_data(id int)")
	must(t, err)
	ownerCfg := cfg.Copy()
	ownerCfg.ConnConfig.User = roles[0]
	ownerCfg.ConnConfig.Password = password
	ownerDB, err = pgxpool.NewWithConfig(t.Context(), ownerCfg)
	must(t, err)
	must(t, challenge.Migrate(t.Context(), ownerDB))
	must(t, challenge.Migrate(t.Context(), ownerDB))
	role := pgx.Identifier{roles[1]}.Sanitize()
	_, err = db.Exec(t.Context(), "GRANT USAGE ON SCHEMA challenge TO "+role+"; GRANT SELECT ON challenge.schema_migrations TO "+role+"; GRANT SELECT,INSERT,UPDATE ON challenge.runs,challenge.claims,challenge.receipts,challenge.model_calls TO "+role+"; GRANT INSERT ON challenge.audit_events TO "+role+"; GRANT USAGE ON ALL SEQUENCES IN SCHEMA challenge TO "+role)
	must(t, err)
	runtimeCfg := cfg.Copy()
	runtimeCfg.ConnConfig.User = roles[1]
	runtimeCfg.ConnConfig.Password = password
	runtimeDB, err = pgxpool.NewWithConfig(t.Context(), runtimeCfg)
	must(t, err)
	must(t, challenge.Ready(t.Context(), runtimeDB))
	for _, sql := range []string{"SELECT * FROM unrelated.private_data", "CREATE TABLE challenge.forbidden(id int)", "UPDATE challenge.schema_migrations SET checksum='forged'", "DELETE FROM challenge.audit_events"} {
		if _, err = runtimeDB.Exec(t.Context(), sql); err == nil {
			t.Fatalf("runtime permission exceeded: %s", sql)
		}
	}
	l := &lab{t: t, db: db, pki: newPKI(t), deps: &dependencies{channels: map[string]string{}, receipts: map[string]*content.RegistrationReceipt{}, allowed: true, preferenceValid: true}}
	d := l.deps
	ia := l.pki.serve("identity", func(s *grpc.Server) { identity.RegisterIdentityServiceServer(s, d) }, false)
	ca := l.pki.serve("content", func(s *grpc.Server) { content.RegisterContentServiceServer(s, d) }, false)
	aa := l.pki.serve("asset", func(s *grpc.Server) { asset.RegisterAssetServiceServer(s, d) }, false)
	va := l.pki.serve("voting", func(s *grpc.Server) { voting.RegisterVotingServiceServer(s, d) }, false)
	for i := range l.services {
		l.services[i], err = challenge.NewServer(runtimeDB, challenge.Config{Models: map[string]bool{"fixture-model": true}, Harnesses: map[string]bool{"fixture": true}}, identity.NewIdentityServiceClient(l.pki.dial(ia, "identity", "challenge")), content.NewContentServiceClient(l.pki.dial(ca, "content", "challenge")), asset.NewAssetServiceClient(l.pki.dial(aa, "asset", "challenge")), voting.NewVotingServiceClient(l.pki.dial(va, "voting", "challenge")))
		must(t, err)
		address := l.pki.serve("challenge", func(s *grpc.Server) { pb.RegisterChallengeServiceServer(s, l.services[i]) }, true)
		l.admins[i] = pb.NewChallengeServiceClient(l.pki.dial(address, "challenge", "gateway"))
		l.workers[i] = pb.NewChallengeServiceClient(l.pki.dial(address, "challenge", "challenge-worker"))
	}
	l.identity = identity.NewIdentityServiceClient(l.pki.dial(ia, "identity", "gateway"))
	l.web = httptest.NewUnstartedServer(nil)
	handler, err := gateway.New(l.identity, gateway.Options{Origin: "https://" + l.web.Listener.Addr().String(), Challenge: l.admins[0]})
	must(t, err)
	l.web.Config.Handler = handler
	l.web.StartTLS()
	t.Cleanup(l.web.Close)
	return l
}
func config() *pb.RunConfiguration {
	return &pb.RunConfiguration{InitialTaskPackage: &pb.InitialTaskPackage{TaskRevision: 1, Description: "管理员固定执行说明", WorkRequirements: "提交可评测文字作品"}, Packer: &pb.ModelConfiguration{Model: "fixture-model"}, Ranker: &pb.ModelConfiguration{Model: "fixture-model", Prompt: "baseline_reverse"}, Executor: &pb.ExecutorConfiguration{Model: "fixture-model", Harness: "fixture"}, RankerFitThreshold: .8, RankerMinComparablePairs: 1, RankerRoundLimit: 2, RoundLimit: 2, Budget: &pb.Budget{Amount: 100, Unit: "model_calls"}}
}
func (l *lab) start(c *pb.RunConfiguration) *pb.Run {
	l.t.Helper()
	out, err := l.admins[0].StartRun(l.t.Context(), &pb.StartRunRequest{ActorAssertion: "admin", TaskId: "target", Configuration: c, IdempotencyKey: fmt.Sprintf("key_%d", time.Now().UnixNano())})
	must(l.t, err)
	return out.Run
}
func (l *lab) get(id string) *pb.Run {
	l.t.Helper()
	r, err := l.admins[1].GetRun(l.t.Context(), &pb.GetRunRequest{ActorAssertion: "admin", RunId: id})
	must(l.t, err)
	return r.Run
}
func (l *lab) claim(client int) *pb.Assignment {
	l.t.Helper()
	r, err := l.workers[client].ClaimWork(l.t.Context(), &pb.ClaimWorkRequest{WorkerInstanceId: fmt.Sprintf("worker_%d", client), ClaimRequestId: fmt.Sprintf("claim_%d", time.Now().UnixNano()), Capabilities: []pb.WorkKind{1, 2, 3, 4, 5, 6, 7}})
	must(l.t, err)
	return r.Assignment
}
func (l *lab) activate(a *pb.Assignment) string {
	l.t.Helper()
	r, err := l.workers[0].ActivateAttempt(l.t.Context(), &pb.ActivateAttemptRequest{Attempt: a.Attempt, ExecutionInstanceId: "process_1"})
	must(l.t, err)
	return r.Grant
}
func rankings(input *pb.RankInput, reverse bool) *pb.Rankings {
	out := &pb.Rankings{}
	for _, sample := range input.Samples {
		r := &pb.Ranking{TaskId: sample.Task.TaskId}
		for _, w := range sample.Works {
			r.OrderedWorkIds = append(r.OrderedWorkIds, w.Id)
			r.Judgments = append(r.Judgments, &pb.Judgment{WorkId: w.Id, Criteria: "准确性", Rationale: "根据作品内容比较", EvidenceRefs: []string{w.Id}})
		}
		if reverse {
			for i, j := 0, len(r.OrderedWorkIds)-1; i < j; i, j = i+1, j-1 {
				r.OrderedWorkIds[i], r.OrderedWorkIds[j] = r.OrderedWorkIds[j], r.OrderedWorkIds[i]
			}
		}
		out.Items = append(out.Items, r)
	}
	return out
}
func (l *lab) complete(a *pb.Assignment, result *pb.CompleteWorkRequest) {
	l.t.Helper()
	result.Attempt = a.Attempt
	result.ResultDigest = challenge.ResultDigest(result)
	_, err := l.workers[0].CompleteWork(l.t.Context(), result)
	must(l.t, err)
}

// fixtureExecutor 只用于模块测试，绝不声称提供了真实执行器或沙箱隔离。
type fixtureExecutor struct{ d *dependencies }

func (e fixtureExecutor) Execute(_ context.Context, a *pb.Assignment, _ string) (*pb.GeneratedWork, error) {
	data, _ := protojson.Marshal(a)
	if strings.Contains(string(data), privateComment) || a.GetExecute() == nil {
		return nil, fmt.Errorf("executor input leaked")
	}
	e.d.mu.Lock()
	e.d.executions++
	e.d.mu.Unlock()
	return &pb.GeneratedWork{Work: &content.Work{Id: "work_" + a.Attempt.AttemptId, Artifacts: []*content.Artifact{{Value: &content.Artifact_Text{Text: "独立完成的作品"}}}}, Report: "关键决策：按初始要求完成；最终结果：文字作品。"}, nil
}

func (l *lab) modelProvider() *challengeworker.Provider {
	l.t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer fixture-secret" {
			http.Error(w, "bad request", 400)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			l.t.Error(err)
			return
		}
		l.deps.mu.Lock()
		l.deps.modelCalls++
		l.deps.requests = append(l.deps.requests, string(data))
		l.deps.mu.Unlock()
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.Unmarshal(data, &request) != nil {
			http.Error(w, "invalid", 400)
			return
		}
		system := request.Messages[0].Content
		raw := strings.Split(request.Messages[1].Content, "\n文件清单：")[0]
		var result proto.Message
		switch {
		case strings.Contains(system, "改进后的通用排序规则"):
			result = &pb.PromptResult{Prompt: "按准确性排序，不受作品位置影响。"}
		case strings.Contains(system, "改进后的执行提示"):
			if strings.Contains(raw, privateComment) || strings.Contains(raw, "humanCounts") {
				l.t.Error("P received forbidden material")
			}
			result = &pb.PromptResult{Prompt: "逐项检查任务要求，完成后核验结果。"}
		case strings.Contains(system, "仅依据任务和自身作品"):
			if strings.Contains(raw, privateComment) || strings.Contains(raw, "准确完整的作品") {
				l.t.Error("P explanation received forbidden material")
			}
			in := &pb.ExplainInput{}
			if protojson.Unmarshal([]byte(raw), in) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			result = &pb.Judgment{WorkId: in.OwnWork.Id, Criteria: "准确性", Rationale: "自身作品符合任务要求。", EvidenceRefs: []string{in.OwnWork.Id}}
		default:
			in := &pb.RankInput{}
			if protojson.Unmarshal([]byte(raw), in) != nil {
				http.Error(w, "invalid", 400)
				return
			}
			if strings.Contains(raw, "humanCounts") {
				l.t.Error("R ranking received labels")
			}
			result = rankings(in, strings.Contains(system, "baseline_reverse"))
		}
		body, err := protojson.Marshal(result)
		if err != nil {
			l.t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": string(body)}}}})
	}))
	l.t.Cleanup(server.Close)
	p, err := challengeworker.NewProvider(challengeworker.ProviderConfig{BaseURL: server.URL, Model: "fixture-model", Protocol: "completion", APIKey: "fixture-secret"}, server.Client())
	must(l.t, err)
	return p
}

func TestChallengeEndToEnd(t *testing.T) {
	l := newLab(t)
	l.deps.loseRegistration = true
	cfg := config()
	cfg.Packer.Prompt = strings.Repeat("p", 9000)
	cfg.Executor.Prompt = strings.Repeat("e", 9000)
	configuration, _ := protojson.Marshal(cfg)
	requestBody, _ := json.Marshal(map[string]any{"taskId": "target", "configuration": json.RawMessage(configuration)})
	req, _ := http.NewRequest("POST", l.web.URL+"/api/admin/challenge-runs", bytes.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", l.web.URL)
	req.Header.Set("X-CSRF-Token", "csrf")
	req.Header.Set("Idempotency-Key", "http_start")
	req.AddCookie(&http.Cookie{Name: gateway.SessionCookie, Value: "admin"})
	response, err := l.web.Client().Do(req)
	must(t, err)
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatalf("start %d: %s", response.StatusCode, body)
	}
	var started map[string]any
	must(t, json.Unmarshal(body, &started))
	for _, role := range []string{"packer", "ranker", "executor"} {
		if _, ok := started["configuration"].(map[string]any)[role].(map[string]any)["parameters"].(map[string]any); !ok {
			t.Fatal("HTTP parameters must match OpenAPI object schema")
		}
	}
	runID := started["id"].(string)
	worker := &challengeworker.Worker{Client: l.workers[0], Provider: l.modelProvider(), Executor: fixtureExecutor{l.deps}, Instance: "worker_0"}
	for step := 0; step < 40; step++ {
		_ = l.services[step%2].Reconcile(t.Context())
		run := l.get(runID)
		if run.Status == "completed" {
			break
		}
		if run.Status == "failed" {
			t.Fatalf("unexpected failure: %s", run.FailureReason)
		}
		before := l.deps.executions
		worked, err := worker.RunOnce(t.Context())
		must(t, err)
		if run.Stage == "optimizing_ranker" && l.deps.executions != before {
			t.Fatal("E ran before R qualified")
		}
		if !worked && run.Stage != "registering" && run.Status != "finalizing" {
			t.Fatalf("stalled: %s %s", run.Status, run.Stage)
		}
	}
	run := l.get(runID)
	if run.Status != "completed" || run.RankerRound != 1 || run.Round != 2 || run.RankerFit.Status != "passed" || run.RankerFit.GetScore() != 1 || l.deps.executions != 2 || l.deps.registrations != 1 {
		t.Fatalf("unexpected final state: %+v, E=%d registrations=%d", run, l.deps.executions, l.deps.registrations)
	}
	if len(run.Candidates) != 2 || run.Candidates[0].RegistrationState != "discarded" || run.Candidates[1].RegistrationState != "registered" {
		t.Fatal("only last candidate should register")
	}
	if l.deps.channels[runID] != "closed" {
		t.Fatal("completed before registration closed")
	}
	_, err = l.admins[1].RegisterCandidate(t.Context(), &pb.RegisterCandidateRequest{ActorAssertion: "admin", RunId: runID, CandidateId: run.Candidates[1].Id})
	must(t, err)
	if l.deps.registrations != 1 {
		t.Fatal("duplicate registration")
	}
	request, _ := http.NewRequest("GET", l.web.URL+"/api/admin/challenge-runs/"+runID, nil)
	request.AddCookie(&http.Cookie{Name: gateway.SessionCookie, Value: "admin"})
	response, err = l.web.Client().Do(request)
	must(t, err)
	body, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	for _, secret := range []string{privateComment, "countsByWork", "reservedCalls", "fitBinding", "grant_", "fixture-secret"} {
		if bytes.Contains(body, []byte(secret)) {
			t.Fatalf("public response contains %q", secret)
		}
	}
}

func TestChallengeFencingAndCancellation(t *testing.T) {
	l := newLab(t)
	run := l.start(config())
	must(t, l.services[0].Reconcile(t.Context()))
	var claimed [2]*pb.ClaimWorkResponse
	var errs [2]error
	var wg sync.WaitGroup
	for i := range claimed {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			claimed[i], errs[i] = l.workers[i].ClaimWork(t.Context(), &pb.ClaimWorkRequest{WorkerInstanceId: fmt.Sprintf("worker_%d", i), ClaimRequestId: "same_claim", Capabilities: []pb.WorkKind{1, 2, 3, 4, 5, 6, 7}})
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		must(t, err)
	}
	winner := -1
	for i, v := range claimed {
		if v.Assignment != nil {
			if winner != -1 {
				t.Fatal("two workers leased same Run")
			}
			winner = i
		}
	}
	if winner < 0 {
		t.Fatal("no claimant")
	}
	a := claimed[winner].Assignment
	_, err := l.workers[1-winner].RenewLease(t.Context(), &pb.RenewLeaseRequest{Attempt: a.Attempt})
	code(t, err, codes.FailedPrecondition)
	retry, err := l.workers[winner].ClaimWork(t.Context(), &pb.ClaimWorkRequest{WorkerInstanceId: fmt.Sprintf("worker_%d", winner), ClaimRequestId: "same_claim", Capabilities: []pb.WorkKind{1, 2, 3, 4, 5, 6, 7}})
	must(t, err)
	if !proto.Equal(a, retry.Assignment) {
		t.Fatal("claim retry changed assignment")
	}
	l.deps.closeUnavailable = true
	_, err = l.admins[0].CancelRun(t.Context(), &pb.CancelRunRequest{ActorAssertion: "admin", RunId: run.Id, Reason: "测试取消"})
	must(t, err)
	if l.services[1].Reconcile(t.Context()) == nil {
		t.Fatal("expected close failure")
	}
	if l.get(run.Id).Status != "cancelling" {
		t.Fatal("cancelled before close acknowledgement")
	}
	result := &pb.CompleteWorkRequest{Attempt: a.Attempt, Result: &pb.CompleteWorkRequest_Rankings{Rankings: rankings(a.GetRank(), false)}}
	result.ResultDigest = challenge.ResultDigest(result)
	_, err = l.workers[winner].CompleteWork(t.Context(), result)
	code(t, err, codes.FailedPrecondition)
	l.deps.closeUnavailable = false
	must(t, l.services[0].Reconcile(t.Context()))
	if l.get(run.Id).Status != "cancelled" {
		t.Fatal("cancel not completed")
	}
	opened, err := l.deps.OpenRunRegistration(t.Context(), &content.OpenRunRegistrationRequest{RunId: run.Id, TaskId: "target"})
	must(t, err)
	if opened.State != "closed" {
		t.Fatal("late open resurrected channel")
	}
}

func TestChallengeBudgetAndRoleBoundaries(t *testing.T) {
	l := newLab(t)
	for _, actor := range []string{"", "user", "mcp"} {
		_, err := l.admins[0].StartRun(t.Context(), &pb.StartRunRequest{ActorAssertion: actor, TaskId: "target", Configuration: config(), IdempotencyKey: "bad"})
		if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unauthenticated {
			t.Fatalf("actor %q: %v", actor, err)
		}
	}
	q := &pb.StartRunRequest{ActorAssertion: "admin", TaskId: "target", Configuration: config(), IdempotencyKey: "once"}
	first, err := l.admins[0].StartRun(t.Context(), q)
	must(t, err)
	second, err := l.admins[1].StartRun(t.Context(), q)
	must(t, err)
	if first.Run.Id != second.Run.Id {
		t.Fatal("duplicate start")
	}
	q.Configuration.RoundLimit++
	_, err = l.admins[0].StartRun(t.Context(), q)
	code(t, err, codes.Aborted)
	must(t, l.services[0].Reconcile(t.Context()))
	a := l.claim(0)
	grant := l.activate(a)
	_, err = l.workers[0].ReadMaterial(t.Context(), &pb.ReadMaterialRequest{Attempt: a.Attempt, MaterialId: "comment_1"})
	if err == nil {
		stream, _ := l.workers[0].ReadMaterial(t.Context(), &pb.ReadMaterialRequest{Attempt: a.Attempt, MaterialId: "comment_1"})
		_, err = stream.Recv()
	}
	code(t, err, codes.PermissionDenied)
	call := &pb.ReserveModelCallRequest{Attempt: a.Attempt, Grant: grant, Model: "fixture-model", RequestId: "call_once", RequestDigest: strings.Repeat("a", 64)}
	reservation, err := l.workers[0].ReserveModelCall(t.Context(), call)
	must(t, err)
	if !reservation.DispatchPermit {
		t.Fatal("missing first dispatch")
	}
	retry, err := l.workers[0].ReserveModelCall(t.Context(), call)
	must(t, err)
	if retry.DispatchPermit {
		t.Fatal("duplicate dispatch")
	}
	call.RequestId = "bypass_unknown"
	_, err = l.workers[0].ReserveModelCall(t.Context(), call)
	code(t, err, codes.FailedPrecondition)
	_, err = l.workers[0].SettleModelCall(t.Context(), &pb.SettleModelCallRequest{Attempt: a.Attempt, ReservationId: reservation.ReservationId, Outcome: "succeeded"})
	must(t, err)
	result := &pb.CompleteWorkRequest{Result: &pb.CompleteWorkRequest_Rankings{Rankings: rankings(a.GetRank(), false)}}
	l.complete(a, result)
	_, err = l.workers[0].CompleteWork(t.Context(), result)
	must(t, err)
	packer := l.claim(0)
	if packer.Kind != pb.WorkKind_WORK_KIND_PACK_TASK {
		t.Fatal(packer.Kind)
	}
	raw, _ := protojson.Marshal(packer)
	if bytes.Contains(raw, []byte(privateComment)) || bytes.Contains(raw, []byte("准确完整的作品")) || bytes.Contains(raw, []byte("humanCounts")) {
		t.Fatal("P input leaked")
	}
	l.deps.preferenceValid = false
	_, err = l.workers[0].ActivateAttempt(t.Context(), &pb.ActivateAttemptRequest{Attempt: packer.Attempt, ExecutionInstanceId: "new_process"})
	code(t, err, codes.FailedPrecondition)
}
