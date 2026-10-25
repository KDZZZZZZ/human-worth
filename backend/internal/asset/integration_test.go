//go:build integration

package asset

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
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	assetapp "github.com/KDZZZZZZ/human-worth/backend/internal/asset/application"
	assetpostgres "github.com/KDZZZZZZ/human-worth/backend/internal/asset/repository/postgres"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type assetIntegration struct {
	t         *testing.T
	admin     *pgxpool.Pool
	db        *pgxpool.Pool
	runtimeDB *pgxpool.Pool
	storage   FileStorage
	pki       *assetTestPKI
	identity  *fakeIdentityClient
}

func browserUploadUseCase(db *pgxpool.Pool, storage Storage) assetapp.BrowserUploadUseCase {
	useCase := assetapp.BrowserUploadUseCase{Repository: assetpostgres.UploadRepository{DB: db}, Storage: storage, ChunkBytes: maxChunkBytes, MaxAssetBytes: maxAssetBytes}
	if multipart, ok := storage.(assetapp.MultipartStorage); ok {
		useCase.Multipart = multipart
	}
	return useCase
}

func uploadSessionUseCase(db *pgxpool.Pool) assetapp.SessionUseCase {
	return assetapp.SessionUseCase{Repository: assetpostgres.UploadRepository{DB: db}}
}

func completeUploadUseCase(db *pgxpool.Pool, storage Storage) assetapp.CompleteUploadUseCase {
	return assetapp.CompleteUploadUseCase{Repository: assetpostgres.UploadRepository{DB: db}, Storage: storage}
}

func abortUploadUseCase(db *pgxpool.Pool, storage Storage) assetapp.AbortUploadUseCase {
	return assetapp.AbortUploadUseCase{Repository: assetpostgres.UploadRepository{DB: db}, Storage: storage}
}

func challengeUploadUseCase(db *pgxpool.Pool, storage Storage) assetapp.ChallengeUploadUseCase {
	useCase := assetapp.ChallengeUploadUseCase{Repository: assetpostgres.UploadRepository{DB: db}, Storage: storage, ChunkBytes: maxChunkBytes, MaxAssetBytes: maxAssetBytes}
	if multipart, ok := storage.(assetapp.ChallengeMultipartStorage); ok {
		useCase.Multipart = multipart
	}
	return useCase
}

func TestAssetPostgresIntegration(t *testing.T) {
	dsn := testDatabaseURL(t)
	if dsn == "" {
		t.Skip("INCONCLUSIVE: ASSET_TEST_DATABASE_URL[_FILE] is not configured")
	}
	fixture := newAssetIntegration(t, dsn)
	t.Run("private_upload_persists_and_recovers", fixture.testPrivateUploadPersistsAndRecovers)
	t.Run("owner_isolation_and_rejection", fixture.testOwnerIsolationAndRejection)
	t.Run("idempotency_and_concurrent_offsets", fixture.testIdempotencyAndConcurrentOffsets)
	t.Run("challenge_grpc_scope", fixture.testChallengeGRPCScope)
	t.Run("runtime_database_boundary", fixture.testRuntimeDatabaseBoundary)
}

func testDatabaseURL(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("ASSET_TEST_DATABASE_URL_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("INCONCLUSIVE: read ASSET_TEST_DATABASE_URL_FILE: %v", err)
		}
		return strings.TrimSpace(string(data))
	}
	return strings.TrimSpace(os.Getenv("ASSET_TEST_DATABASE_URL"))
}

func newAssetIntegration(t *testing.T, dsn string) *assetIntegration {
	t.Helper()
	ctx := t.Context()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect test PostgreSQL: %v", err)
	}
	if err = admin.Ping(ctx); err != nil {
		admin.Close()
		t.Fatalf("ping test PostgreSQL: %v", err)
	}
	name := fmt.Sprintf("hw_asset_%d", time.Now().UnixNano())
	ownerRole := name + "_owner"
	runtimeRole := name + "_runtime"
	password := hex.EncodeToString(randomBytes(t, 24))
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		admin.Close()
		t.Fatalf("create isolated database: %v", err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	cfg.ConnConfig.Database = name
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect isolated database: %v", err)
	}
	cleanup := func() {
		db.Close()
		admin.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{ownerRole}.Sanitize())
		admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+pgx.Identifier{runtimeRole}.Sanitize())
		admin.Close()
	}
	t.Cleanup(cleanup)
	roleOwner := pgx.Identifier{ownerRole}.Sanitize()
	roleRuntime := pgx.Identifier{runtimeRole}.Sanitize()
	if _, err = db.Exec(ctx, "REVOKE CREATE ON DATABASE "+pgx.Identifier{name}.Sanitize()+" FROM PUBLIC; CREATE ROLE "+roleOwner+" LOGIN PASSWORD '"+password+"'; CREATE ROLE "+roleRuntime+" LOGIN PASSWORD '"+password+"'; CREATE SCHEMA asset AUTHORIZATION "+roleOwner+"; CREATE SCHEMA unrelated; CREATE TABLE unrelated.private_data(secret text); CREATE SCHEMA identity; CREATE TABLE identity.private_data(secret text); CREATE SCHEMA content; CREATE TABLE content.private_data(secret text)"); err != nil {
		t.Fatalf("prepare isolated database: %v", err)
	}
	ownerCfg := cfg.Copy()
	ownerCfg.ConnConfig.User = ownerRole
	ownerCfg.ConnConfig.Password = password
	ownerDB, err := pgxpool.NewWithConfig(ctx, ownerCfg)
	if err != nil {
		t.Fatalf("connect migration role: %v", err)
	}
	t.Cleanup(ownerDB.Close)
	if err = Migrate(ctx, ownerDB); err != nil {
		t.Fatalf("asset migration: %v", err)
	}
	if err = Migrate(ctx, ownerDB); err != nil {
		t.Fatalf("asset migration is not repeatable: %v", err)
	}
	if _, err = ownerDB.Exec(ctx, "CREATE SCHEMA should_be_denied"); err == nil {
		t.Fatal("migration role unexpectedly has database-wide CREATE")
	}
	if _, err = db.Exec(ctx, "GRANT USAGE ON SCHEMA asset TO "+roleRuntime+"; GRANT SELECT ON asset.schema_migrations,asset.assets,asset.upload_sessions TO "+roleRuntime+"; GRANT INSERT ON asset.assets,asset.upload_sessions TO "+roleRuntime+"; GRANT UPDATE(state,detected_media_type,received_bytes,actual_sha256,completed_at,updated_at,storage_upload_id,storage_parts,storage_cleaned_at) ON asset.assets TO "+roleRuntime+"; GRANT UPDATE(state,received_bytes,updated_at,storage_cleaned_at) ON asset.upload_sessions TO "+roleRuntime); err != nil {
		t.Fatalf("grant runtime permissions: %v", err)
	}
	runtimeCfg := cfg.Copy()
	runtimeCfg.ConnConfig.User = runtimeRole
	runtimeCfg.ConnConfig.Password = password
	runtimeCfg.MaxConns = 8
	runtimeDB, err := pgxpool.NewWithConfig(ctx, runtimeCfg)
	if err != nil {
		t.Fatalf("connect runtime role: %v", err)
	}
	t.Cleanup(runtimeDB.Close)
	if err = runtimeDB.Ping(ctx); err != nil {
		t.Fatalf("ping runtime database: %v", err)
	}
	return &assetIntegration{
		t: t, admin: admin, db: db, runtimeDB: runtimeDB,
		storage: FileStorage{Root: t.TempDir()}, pki: newAssetTestPKI(t),
		identity: &fakeIdentityClient{revoked: map[string]bool{}},
	}
}

func (f *assetIntegration) start(caller string) (pb.AssetServiceClient, func()) {
	return f.startStorage(caller, f.storage)
}
func (f *assetIntegration) startStorage(caller string, storage Storage) (pb.AssetServiceClient, func()) {
	f.t.Helper()
	serverCert, serverKey, ca := f.pki.cert(f.t, "asset")
	tlsConfig, err := platform.TLS(serverCert, serverKey, ca, "")
	if err != nil {
		f.t.Fatal(err)
	}
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.UnaryInterceptor(Authorization), grpc.StreamInterceptor(AuthorizationStream), grpc.MaxRecvMsgSize(platform.AssetUploadMessageBytes))
	pb.RegisterAssetServiceServer(grpcServer, &Server{DB: f.runtimeDB, Identity: f.identity, Storage: storage, BrowserUpload: browserUploadUseCase(f.runtimeDB, storage), UploadSessions: uploadSessionUseCase(f.runtimeDB), CompleteUseCase: completeUploadUseCase(f.runtimeDB, storage), AbortUseCase: abortUploadUseCase(f.runtimeDB, storage), ChallengeUpload: challengeUploadUseCase(f.runtimeDB, storage)})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	go grpcServer.Serve(listener)
	clientCert, clientKey, _ := f.pki.cert(f.t, caller)
	clientTLS, err := platform.TLS(clientCert, clientKey, ca, "asset")
	if err != nil {
		f.t.Fatal(err)
	}
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(512<<10), grpc.MaxCallSendMsgSize(platform.AssetUploadMessageBytes)))
	if err != nil {
		grpcServer.Stop()
		listener.Close()
		f.t.Fatal(err)
	}
	stop := func() {
		conn.Close()
		grpcServer.Stop()
		listener.Close()
	}
	return pb.NewAssetServiceClient(conn), stop
}

func (f *assetIntegration) testPrivateUploadPersistsAndRecovers(t *testing.T) {
	client, stop := f.start("gateway")
	defer stop()
	data := bytes.Repeat([]byte("a"), maxChunkBytes+23)
	digest := sha256.Sum256(data)
	created, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "asset-integration-key-01", OriginalFilename: "hello.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(data)), ExpectedSha256: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	if created.UploadSession.State != pb.UploadSessionState_UPLOAD_SESSION_STATE_ACTIVE || created.Asset.State != pb.AssetState_ASSET_STATE_STAGING {
		t.Fatalf("unexpected initial state: session=%v asset=%v", created.UploadSession.State, created.Asset.State)
	}
	if created.Asset.OwnerId != "account_alice" {
		t.Fatalf("request could not override assertion owner: %q", created.Asset.OwnerId)
	}
	first := data[:maxChunkBytes]
	second := data[maxChunkBytes:]
	response, err := browserUpload(t, client, created.UploadSession.Id, "alice", first)
	if err != nil {
		t.Fatal(err)
	}
	if response.ReceivedBytes != int64(len(first)) || response.NextOffset != int64(len(first)) {
		t.Fatalf("unexpected first-chunk progress: %+v", response)
	}
	progress, err := client.GetUploadSession(t.Context(), &pb.GetUploadSessionRequest{ActorAssertion: "alice", UploadSessionId: created.UploadSession.Id})
	if err != nil {
		t.Fatal(err)
	}
	if progress.UploadSession.ReceivedBytes != int64(len(first)) || progress.UploadSession.ExpectedBytes != int64(len(data)) {
		t.Fatalf("unexpected persisted first-chunk progress: %+v", progress.UploadSession)
	}
	response, err = browserUploadAt(t, client, created.UploadSession.Id, "alice", int64(len(first)), second)
	if err != nil {
		t.Fatal(err)
	}
	if response.ReceivedBytes != int64(len(data)) || response.NextOffset != int64(len(data)) {
		t.Fatalf("unexpected resumed progress: %+v", response)
	}
	completed, err := client.CompleteUpload(t.Context(), &pb.CompleteUploadRequest{ActorAssertion: "alice", UploadSessionId: created.UploadSession.Id, AssetId: created.Asset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Asset.State != pb.AssetState_ASSET_STATE_READY || completed.Asset.Sha256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected completed asset: %+v", completed.Asset)
	}
	var assetState, sessionState string
	var received, expected, sessionReceived int64
	var actualHash []byte
	var staging, final string
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT state,received_bytes,expected_bytes,actual_sha256,staging_key,final_key FROM asset.assets WHERE id=$1", created.Asset.Id).Scan(&assetState, &received, &expected, &actualHash, &staging, &final); err != nil {
		t.Fatal(err)
	}
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT state,received_bytes FROM asset.upload_sessions WHERE id=$1", created.UploadSession.Id).Scan(&sessionState, &sessionReceived); err != nil {
		t.Fatal(err)
	}
	if assetState != "ready" || sessionState != "completed" || received != int64(len(data)) || expected != int64(len(data)) || sessionReceived != int64(len(data)) || !bytesEqual(actualHash, digest[:]) {
		t.Fatalf("bad database state: asset=%s session=%s received=%d expected=%d session_received=%d hash=%x", assetState, sessionState, received, expected, sessionReceived, actualHash)
	}
	if _, err = f.storage.Stat(t.Context(), staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging object still exists: %v", err)
	}
	if size, statErr := f.storage.Stat(t.Context(), final); statErr != nil || size != int64(len(data)) {
		t.Fatalf("final object size=%d err=%v", size, statErr)
	}
	got := download(t, client, "alice", created.Asset.Id)
	if string(got) != string(data) || sha256Hex(got) != completed.Asset.Sha256 {
		t.Fatalf("download mismatch: %q", got)
	}
	stop()
	client, stop = f.start("gateway")
	defer stop()
	metadata, err := client.GetAssetMetadata(t.Context(), &pb.GetAssetMetadataRequest{ActorAssertion: "alice", AssetId: created.Asset.Id})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Asset.State != pb.AssetState_ASSET_STATE_READY {
		t.Fatalf("restarted service returned %v", metadata.Asset.State)
	}
	if got = download(t, client, "alice", created.Asset.Id); string(got) != string(data) {
		t.Fatalf("restarted download mismatch: %q", got)
	}
}

func (f *assetIntegration) testOwnerIsolationAndRejection(t *testing.T) {
	client, stop := f.start("gateway")
	defer stop()
	data := []byte("owner-isolation")
	digest := sha256.Sum256(data)
	created, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "owner-isolation-key-01", OriginalFilename: "private.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(data)), ExpectedSha256: hex.EncodeToString(digest[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = browserUpload(t, client, created.UploadSession.Id, "alice", data); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CompleteUpload(t.Context(), &pb.CompleteUploadRequest{ActorAssertion: "alice", UploadSessionId: created.UploadSession.Id}); err != nil {
		t.Fatal(err)
	}
	for name, call := range map[string]func() error{
		"metadata": func() error {
			_, e := client.GetAssetMetadata(t.Context(), &pb.GetAssetMetadataRequest{ActorAssertion: "bob", AssetId: created.Asset.Id})
			return e
		},
		"download": func() error {
			return downloadError(t, client, "bob", created.Asset.Id)
		},
		"upload": func() error {
			_, e := browserUpload(t, client, created.UploadSession.Id, "bob", []byte("x"))
			return e
		},
		"session": func() error {
			_, e := client.GetUploadSession(t.Context(), &pb.GetUploadSessionRequest{ActorAssertion: "bob", UploadSessionId: created.UploadSession.Id})
			return e
		},
		"complete": func() error {
			_, e := client.CompleteUpload(t.Context(), &pb.CompleteUploadRequest{ActorAssertion: "bob", UploadSessionId: created.UploadSession.Id})
			return e
		},
	} {
		err := call()
		if status.Code(err) != codes.NotFound {
			t.Errorf("%s status=%s err=%v, want NotFound", name, status.Code(err), err)
		}
	}
	if _, err = client.GetAssetMetadata(t.Context(), &pb.GetAssetMetadataRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing assertion status=%s, want Unauthenticated", status.Code(err))
	}
	for _, tc := range []struct {
		assertion string
		code      codes.Code
	}{{"wrong-audience", codes.PermissionDenied}, {"wrong-method", codes.PermissionDenied}, {"revoked", codes.Unauthenticated}} {
		if _, err = client.GetAssetMetadata(t.Context(), &pb.GetAssetMetadataRequest{ActorAssertion: tc.assertion, AssetId: created.Asset.Id}); status.Code(err) != tc.code {
			t.Errorf("assertion %q status=%s, want %s", tc.assertion, status.Code(err), tc.code)
		}
	}
	revocable, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice-revocable", IdempotencyKey: "revoked-session-key-0001", OriginalFilename: "revoked.txt", DeclaredMediaType: "text/plain", ExpectedBytes: 1, ExpectedSha256: strings.Repeat("0", 64)})
	if err != nil {
		t.Fatal(err)
	}
	f.identity.revoke("alice-revocable")
	if _, err = client.GetUploadSession(t.Context(), &pb.GetUploadSessionRequest{ActorAssertion: "alice-revocable", UploadSessionId: revocable.UploadSession.Id}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("revoked existing session status=%s, want Unauthenticated", status.Code(err))
	}

	if _, err = client.AbortUpload(t.Context(), &pb.AbortUploadRequest{ActorAssertion: "bob", UploadSessionId: created.UploadSession.Id}); status.Code(err) != codes.NotFound {
		t.Errorf("cross-owner abort status=%s", status.Code(err))
	}
	missingID := "ast_00000000000000000000000000000000"
	if _, err = client.GetAssetMetadata(t.Context(), &pb.GetAssetMetadataRequest{ActorAssertion: "alice", AssetId: missingID}); status.Code(err) != codes.NotFound {
		t.Errorf("missing asset status=%s", status.Code(err))
	}
	if err = downloadError(t, client, "alice", missingID); status.Code(err) != codes.NotFound {
		t.Errorf("missing download status=%s", status.Code(err))
	}
}

func (f *assetIntegration) testIdempotencyAndConcurrentOffsets(t *testing.T) {
	client, stop := f.start("gateway")
	defer stop()
	data := []byte("same request")
	digest := sha256.Sum256(data)
	request := &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "idempotency-key-0001", OriginalFilename: "same.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(data)), ExpectedSha256: hex.EncodeToString(digest[:])}
	first, err := client.CreateUploadSession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.CreateUploadSession(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.UploadSession.Id != second.UploadSession.Id || first.Asset.Id != second.Asset.Id {
		t.Fatalf("idempotency returned different resources: %s/%s vs %s/%s", first.UploadSession.Id, first.Asset.Id, second.UploadSession.Id, second.Asset.Id)
	}
	request = protoCloneCreate(request)
	request.ExpectedBytes++
	if _, err = client.CreateUploadSession(t.Context(), request); status.Code(err) != codes.AlreadyExists {
		t.Errorf("different request status=%s, want AlreadyExists", status.Code(err))
	}
	if _, err = browserUploadAt(t, client, first.UploadSession.Id, "alice", 1, []byte("bad")); status.Code(err) != codes.Aborted {
		t.Errorf("wrong offset status=%s, want Aborted", status.Code(err))
	}
	if _, err = browserUpload(t, client, first.UploadSession.Id, "alice", make([]byte, len(data)+1)); status.Code(err) != codes.InvalidArgument {
		t.Errorf("oversize chunk status=%s, want InvalidArgument", status.Code(err))
	}
	progress, err := client.GetUploadSession(t.Context(), &pb.GetUploadSessionRequest{ActorAssertion: "alice", UploadSessionId: first.UploadSession.Id})
	if err != nil {
		t.Fatal(err)
	}
	if progress.UploadSession.ReceivedBytes != 0 {
		t.Fatalf("rejected chunks changed received_bytes=%d", progress.UploadSession.ReceivedBytes)
	}
	var rejectedKey string
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT staging_key FROM asset.assets WHERE id=$1", first.Asset.Id).Scan(&rejectedKey); err != nil {
		t.Fatal(err)
	}
	if _, err = f.storage.Stat(t.Context(), rejectedKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected chunk created staging object: %v", err)
	}
	concurrentData := []byte("ab")
	concurrentHash := sha256.Sum256(concurrentData)
	concurrent, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "concurrent-key-0001", OriginalFilename: "concurrent.txt", DeclaredMediaType: "text/plain", ExpectedBytes: 2, ExpectedSha256: hex.EncodeToString(concurrentHash[:])})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream, streamErr := client.UploadAsset(t.Context())
			if streamErr != nil {
				errs <- streamErr
				return
			}
			_ = stream.Send(&pb.UploadAssetRequest{ActorAssertion: "alice", UploadSessionId: concurrent.UploadSession.Id, Offset: 0, UploadAttemptId: "concurrent-attempt-0001"})
			_ = stream.Send(&pb.UploadAssetRequest{Offset: 0, Chunk: []byte("ab"), ChunkSha256: sha256Hex([]byte("ab"))})
			_, streamErr = stream.CloseAndRecv()
			errs <- streamErr
		}()
	}
	wg.Wait()
	close(errs)
	var success, conflict int
	for err = range errs {
		if err == nil {
			success++
		} else if status.Code(err) == codes.Aborted {
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("concurrent chunk results success=%d conflict=%d", success, conflict)
	}
	progress, err = client.GetUploadSession(t.Context(), &pb.GetUploadSessionRequest{ActorAssertion: "alice", UploadSessionId: concurrent.UploadSession.Id})
	if err != nil {
		t.Fatal(err)
	}
	if progress.UploadSession.ReceivedBytes != 2 {
		t.Fatalf("concurrent writes corrupted received_bytes=%d", progress.UploadSession.ReceivedBytes)
	}
	completeData := []byte("complete twice")
	completeHash := sha256.Sum256(completeData)
	completeSession, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "complete-key-0001", OriginalFilename: "complete.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(completeData)), ExpectedSha256: hex.EncodeToString(completeHash[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = browserUpload(t, client, completeSession.UploadSession.Id, "alice", completeData); err != nil {
		t.Fatal(err)
	}
	completeResults := make(chan *pb.CompleteUploadResponse, 2)
	completeErrors := make(chan error, 2)
	for range 2 {
		go func() {
			result, completeErr := client.CompleteUpload(t.Context(), &pb.CompleteUploadRequest{ActorAssertion: "alice", UploadSessionId: completeSession.UploadSession.Id})
			completeResults <- result
			completeErrors <- completeErr
		}()
	}
	var completed []*pb.CompleteUploadResponse
	for range 2 {
		if completeErr := <-completeErrors; completeErr != nil {
			t.Fatal(completeErr)
		}
		completed = append(completed, <-completeResults)
	}
	if completed[0].Asset.Id != completed[1].Asset.Id || completed[0].Asset.State != pb.AssetState_ASSET_STATE_READY || completed[1].Asset.State != pb.AssetState_ASSET_STATE_READY {
		t.Fatalf("concurrent complete changed result: %s vs %s", completed[0].Asset.Id, completed[1].Asset.Id)
	}
	var readyCount int
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT count(*) FROM asset.assets WHERE id=$1 AND state='ready'", completeSession.Asset.Id).Scan(&readyCount); err != nil {
		t.Fatal(err)
	}
	if readyCount != 1 {
		t.Fatalf("concurrent complete created %d ready rows", readyCount)
	}
	abortedData := []byte("abort me")
	abortedHash := sha256.Sum256(abortedData)
	aborted, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "abort-upload-key-0001", OriginalFilename: "abort.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(abortedData)), ExpectedSha256: hex.EncodeToString(abortedHash[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = browserUpload(t, client, aborted.UploadSession.Id, "alice", abortedData); err != nil {
		t.Fatal(err)
	}
	if _, err = client.AbortUpload(t.Context(), &pb.AbortUploadRequest{ActorAssertion: "alice", UploadSessionId: aborted.UploadSession.Id}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CompleteUpload(t.Context(), &pb.CompleteUploadRequest{ActorAssertion: "alice", UploadSessionId: aborted.UploadSession.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("aborted complete status=%s", status.Code(err))
	}
	var abortedKey string
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT staging_key FROM asset.assets WHERE id=$1", aborted.Asset.Id).Scan(&abortedKey); err != nil {
		t.Fatal(err)
	}
	if _, err = f.storage.Stat(t.Context(), abortedKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted staging object still exists: %v", err)
	}

	expiredData := []byte("expired")
	expiredHash := sha256.Sum256(expiredData)
	expired, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "expired-key-0001", OriginalFilename: "expired.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(expiredData)), ExpectedSha256: hex.EncodeToString(expiredHash[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), "UPDATE asset.upload_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", expired.UploadSession.Id); err != nil {
		t.Fatal(err)
	}
	if _, err = browserUpload(t, client, expired.UploadSession.Id, "alice", expiredData); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expired upload status=%s, want FailedPrecondition", status.Code(err))
	}
	if err = cleanupExpiredForTest(t.Context(), f.runtimeDB, f.storage); err != nil {
		t.Fatal(err)
	}
	var expiredState, expiredAssetState string
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT s.state,a.state FROM asset.upload_sessions s JOIN asset.assets a ON a.id=s.asset_id WHERE s.id=$1", expired.UploadSession.Id).Scan(&expiredState, &expiredAssetState); err != nil {
		t.Fatal(err)
	}
	if expiredState != "expired" || expiredAssetState != "failed" {
		t.Fatalf("cleanup states session=%s asset=%s", expiredState, expiredAssetState)
	}

	mismatchData := []byte("digest mismatch")
	wrongHash := sha256.Sum256([]byte("different"))
	mismatch, err := client.CreateUploadSession(t.Context(), &pb.CreateUploadSessionRequest{ActorAssertion: "alice", IdempotencyKey: "mismatch-key-0001", OriginalFilename: "mismatch.txt", DeclaredMediaType: "text/plain", ExpectedBytes: int64(len(mismatchData)), ExpectedSha256: hex.EncodeToString(wrongHash[:])})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = browserUpload(t, client, mismatch.UploadSession.Id, "alice", mismatchData); err != nil {
		t.Fatal(err)
	}
	if _, err = client.CompleteUpload(t.Context(), &pb.CompleteUploadRequest{ActorAssertion: "alice", UploadSessionId: mismatch.UploadSession.Id}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("hash mismatch status=%s, want FailedPrecondition", status.Code(err))
	}
	var mismatchAssetState, mismatchSessionState, mismatchKey string
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT a.state,s.state,a.staging_key FROM asset.assets a JOIN asset.upload_sessions s ON s.asset_id=a.id WHERE s.id=$1", mismatch.UploadSession.Id).Scan(&mismatchAssetState, &mismatchSessionState, &mismatchKey); err != nil {
		t.Fatal(err)
	}
	if mismatchAssetState != "failed" || mismatchSessionState != "aborted" {
		t.Fatalf("hash mismatch states asset=%s session=%s", mismatchAssetState, mismatchSessionState)
	}
	if _, err = f.storage.Stat(t.Context(), mismatchKey); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("hash mismatch staging object remains: %v", err)
	}
}

func (f *assetIntegration) testChallengeGRPCScope(t *testing.T) {
	challenge, stopChallenge := f.start("challenge")
	defer stopChallenge()
	data := []byte("challenge artifact")
	digest := sha256.Sum256(data)
	access := &pb.Access{TaskId: "tsk_integration_0001", RunId: "run_integration_0001", AttemptId: "attempt_integration_0001", Purpose: "WORK_KIND_EXECUTE_PACKAGE"}
	asset, err := challengeUpload(t, challenge, access, "challenge-upload-0001", data, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if asset.RunId != access.RunId || asset.AttemptId != access.AttemptId || asset.Sha256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("bad challenge asset: %+v", asset)
	}
	if _, err = f.db.Exec(t.Context(), "UPDATE asset.assets SET state='staging',detected_media_type=NULL,received_bytes=0,actual_sha256=NULL,completed_at=NULL WHERE id=$1", asset.Id); err != nil {
		t.Fatal(err)
	}
	recovered, err := challengeUpload(t, challenge, access, "challenge-upload-0001", data, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Id != asset.Id {
		t.Fatalf("final-object recovery changed asset: %s vs %s", recovered.Id, asset.Id)
	}
	got, err := readChallenge(t, challenge, &pb.ReadAssetRequest{AssetId: asset.Id, Access: &pb.Access{TaskId: access.TaskId, RunId: access.RunId, AttemptId: access.AttemptId, Purpose: "output"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("challenge read mismatch: %q", got)
	}
	metadata, err := challenge.GetAsset(t.Context(), &pb.GetAssetRequest{AssetId: asset.Id, Access: &pb.Access{TaskId: access.TaskId, RunId: access.RunId, AttemptId: access.AttemptId, Purpose: "output"}})
	if err != nil || metadata.Asset.Id != asset.Id {
		t.Fatalf("challenge get err=%v asset=%v", err, metadata.GetAsset())
	}
	if _, err = challenge.GetAsset(t.Context(), &pb.GetAssetRequest{AssetId: asset.Id, Access: &pb.Access{TaskId: access.TaskId, RunId: access.RunId, AttemptId: "attempt_other_0001", Purpose: "output"}}); status.Code(err) != codes.NotFound {
		t.Errorf("cross-attempt get status=%s", status.Code(err))
	}
	for name, invalidAccess := range map[string]*pb.Access{
		"task":    {RunId: access.RunId, AttemptId: access.AttemptId, Purpose: "output"},
		"run":     {TaskId: access.TaskId, AttemptId: access.AttemptId, Purpose: "output"},
		"attempt": {TaskId: access.TaskId, RunId: access.RunId, Purpose: "output"},
		"purpose": {TaskId: access.TaskId, RunId: access.RunId, AttemptId: access.AttemptId},
		"unknown": {TaskId: access.TaskId, RunId: access.RunId, AttemptId: access.AttemptId, Purpose: "not-allowed"},
	} {
		if _, err = challenge.GetAsset(t.Context(), &pb.GetAssetRequest{AssetId: asset.Id, Access: invalidAccess}); status.Code(err) != codes.NotFound {
			t.Errorf("invalid %s access status=%s, want NotFound", name, status.Code(err))
		}
	}
	if _, err = readChallenge(t, challenge, &pb.ReadAssetRequest{AssetId: asset.Id, Access: &pb.Access{TaskId: access.TaskId, RunId: access.RunId, AttemptId: "attempt_other_0001", Purpose: "output"}}); status.Code(err) != codes.NotFound {
		t.Errorf("cross-attempt read status=%s, want NotFound", status.Code(err))
	}
	gateway, stopGateway := f.start("gateway")
	defer stopGateway()
	if _, err = gateway.GetAsset(t.Context(), &pb.GetAssetRequest{AssetId: asset.Id, Access: access}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("gateway challenge call status=%s", status.Code(err))
	}
	if _, err = challengeUpload(t, gateway, access, "gateway-cannot-upload", data, hex.EncodeToString(digest[:])); status.Code(err) != codes.PermissionDenied {
		t.Errorf("gateway challenge upload status=%s", status.Code(err))
	}
	if _, err = browserUpload(t, challenge, "upl_00000000000000000000000000000000", "alice", data); status.Code(err) != codes.PermissionDenied {
		t.Errorf("challenge browser assertion upload status=%s", status.Code(err))
	}
	unknown, stopUnknown := f.start("attacker")
	defer stopUnknown()
	if _, err = unknown.GetAssetMetadata(t.Context(), &pb.GetAssetMetadataRequest{ActorAssertion: "alice", AssetId: asset.Id}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("unknown service certificate status=%s", status.Code(err))
	}
	content, stopContent := f.start("content")
	defer stopContent()
	if _, err = content.GetAsset(t.Context(), &pb.GetAssetRequest{AssetId: asset.Id, Access: access}); err != nil {
		t.Errorf("content registration metadata status=%s", status.Code(err))
	}
	if _, err = readChallenge(t, content, &pb.ReadAssetRequest{AssetId: asset.Id, Access: access}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("content must not read challenge bytes: %s", status.Code(err))
	}
	if _, err = content.GetAsset(t.Context(), &pb.GetAssetRequest{AssetId: asset.Id, Access: &pb.Access{TaskId: access.TaskId, RunId: access.RunId, AttemptId: "wrong_attempt_0001", Purpose: "output"}}); status.Code(err) != codes.NotFound {
		t.Errorf("content metadata crossed attempt: %s", status.Code(err))
	}
	if _, err = content.CheckAssetOwnership(t.Context(), &pb.CheckAssetOwnershipRequest{ActorAssertion: "alice", AssetIds: []string{asset.Id}}); status.Code(err) != codes.FailedPrecondition {
		t.Errorf("content ownership method status=%s, want FailedPrecondition rather than service rejection", status.Code(err))
	}
	if _, err = challengeUpload(t, challenge, &pb.Access{TaskId: access.TaskId, RunId: access.RunId, AttemptId: access.AttemptId, Purpose: "output"}, "challenge-upload-invalid", data, hex.EncodeToString(digest[:])); status.Code(err) != codes.InvalidArgument {
		t.Errorf("invalid upload purpose status=%s", status.Code(err))
	}
	again, err := challengeUpload(t, challenge, access, "challenge-upload-0001", data, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if again.Id != asset.Id {
		t.Fatalf("challenge idempotency returned %s instead of %s", again.Id, asset.Id)
	}
	different := sha256.Sum256([]byte("different challenge artifact"))
	if _, err = challengeUpload(t, challenge, access, "challenge-upload-0001", data, hex.EncodeToString(different[:])); status.Code(err) != codes.Aborted {
		t.Errorf("challenge upload conflict status=%s, want Aborted", status.Code(err))
	}
	if _, err = challengeUploadNamed(t, challenge, access, "challenge-upload-0001", "different.txt", data, hex.EncodeToString(digest[:])); status.Code(err) != codes.Aborted {
		t.Errorf("challenge metadata conflict status=%s, want Aborted", status.Code(err))
	}

	interruptedID := "challenge-upload-interrupted"
	stream, err := challenge.UploadAsset(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = stream.Send(&pb.UploadAssetRequest{Access: access, UploadId: interruptedID, Filename: "artifact.txt", MediaType: "text/plain", Bytes: int64(len(data)), Sha256: hex.EncodeToString(digest[:]), Chunk: data[:4]}); err != nil {
		t.Fatal(err)
	}
	if _, err = stream.CloseAndRecv(); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("interrupted challenge upload status=%s", status.Code(err))
	}
	var interruptedAsset string
	if err = f.runtimeDB.QueryRow(t.Context(), "SELECT id FROM asset.assets WHERE run_id=$1 AND attempt_id=$2 AND upload_id=$3", access.RunId, access.AttemptId, interruptedID).Scan(&interruptedAsset); err != nil {
		t.Fatal(err)
	}
	retried, err := challengeUpload(t, challenge, access, interruptedID, data, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if retried.Id != interruptedAsset {
		t.Fatalf("interrupted retry changed asset: %s vs %s", retried.Id, interruptedAsset)
	}
}

func (f *assetIntegration) testRuntimeDatabaseBoundary(t *testing.T) {
	if _, err := f.runtimeDB.Exec(t.Context(), "CREATE TABLE asset.must_not_exist(id int)"); err == nil {
		t.Fatal("runtime role can DDL")
	}
	if _, err := f.runtimeDB.Exec(t.Context(), "UPDATE asset.assets SET owner_id='forged' WHERE false"); err == nil {
		t.Fatal("runtime role can modify owner_id")
	}
	for _, schema := range []string{"identity", "content", "unrelated"} {
		var secret string
		err := f.runtimeDB.QueryRow(t.Context(), "SELECT secret FROM "+pgx.Identifier{schema}.Sanitize()+".private_data").Scan(&secret)
		if err == nil {
			t.Errorf("runtime role read %s schema", schema)
		}
	}
}

func browserUpload(t *testing.T, client pb.AssetServiceClient, sessionID, assertion string, chunks ...[]byte) (*pb.UploadAssetResponse, error) {
	return browserUploadAt(t, client, sessionID, assertion, 0, chunks...)
}

func browserUploadAt(t *testing.T, client pb.AssetServiceClient, sessionID, assertion string, offset int64, chunks ...[]byte) (*pb.UploadAssetResponse, error) {
	t.Helper()
	stream, err := client.UploadAsset(t.Context())
	if err != nil {
		return nil, err
	}
	if err = stream.Send(&pb.UploadAssetRequest{ActorAssertion: assertion, UploadSessionId: sessionID, UploadAttemptId: "browser-attempt-0001", Offset: offset}); err != nil {
		if errors.Is(err, io.EOF) {
			return stream.CloseAndRecv()
		}
		return nil, err
	}
	for _, chunk := range chunks {
		if err = stream.Send(&pb.UploadAssetRequest{Offset: offset, Chunk: chunk, ChunkSha256: sha256Hex(chunk)}); err != nil {
			// A server may reject the metadata before the client sends a chunk.
			// Send then reports EOF; receive the terminal RPC status for assertions.
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		offset += int64(len(chunk))
	}
	return stream.CloseAndRecv()
}

func challengeUpload(t *testing.T, client pb.AssetServiceClient, access *pb.Access, uploadID string, data []byte, digest string) (*pb.Asset, error) {
	return challengeUploadNamed(t, client, access, uploadID, "artifact.txt", data, digest)
}

func challengeUploadNamed(t *testing.T, client pb.AssetServiceClient, access *pb.Access, uploadID, filename string, data []byte, digest string) (*pb.Asset, error) {
	t.Helper()
	stream, err := client.UploadAsset(t.Context())
	if err != nil {
		return nil, err
	}
	if err = stream.Send(&pb.UploadAssetRequest{Access: access, UploadId: uploadID, Filename: filename, MediaType: "text/plain", Bytes: int64(len(data)), Sha256: digest, Chunk: data}); err != nil {
		return nil, err
	}
	response, err := stream.CloseAndRecv()
	if err != nil {
		return nil, err
	}
	return response.Asset, nil
}

func readChallenge(t *testing.T, client pb.AssetServiceClient, request *pb.ReadAssetRequest) ([]byte, error) {
	t.Helper()
	stream, err := client.ReadAsset(t.Context(), request)
	if err != nil {
		return nil, err
	}
	var output []byte
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return output, nil
		}
		if recvErr != nil {
			return nil, recvErr
		}
		output = append(output, chunk.Chunk...)
	}
}

func downloadError(t *testing.T, client pb.AssetServiceClient, assertion, assetID string) error {
	t.Helper()
	stream, err := client.DownloadAsset(t.Context(), &pb.DownloadAssetRequest{ActorAssertion: assertion, AssetId: assetID})
	if err != nil {
		return err
	}
	_, err = stream.Recv()
	return err
}

func download(t *testing.T, client pb.AssetServiceClient, assertion, assetID string) []byte {
	t.Helper()
	stream, err := client.DownloadAsset(t.Context(), &pb.DownloadAssetRequest{ActorAssertion: assertion, AssetId: assetID})
	if err != nil {
		t.Fatal(err)
	}
	var output []byte
	for {
		chunk, recvErr := stream.Recv()
		if errors.Is(recvErr, io.EOF) {
			return output
		}
		if recvErr != nil {
			t.Fatal(recvErr)
		}
		output = append(output, chunk.Data...)
	}
}

func sha256Hex(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }
func randomBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}
func protoCloneCreate(in *pb.CreateUploadSessionRequest) *pb.CreateUploadSessionRequest {
	return proto.Clone(in).(*pb.CreateUploadSessionRequest)
}

type fakeIdentityClient struct {
	identitypb.IdentityServiceClient
	mu      sync.Mutex
	revoked map[string]bool
}

func (c *fakeIdentityClient) revoke(assertion string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revoked[assertion] = true
}

func (c *fakeIdentityClient) VerifyActor(_ context.Context, request *identitypb.VerifyActorRequest, _ ...grpc.CallOption) (*identitypb.VerifyActorResponse, error) {
	c.mu.Lock()
	revoked := c.revoked[request.ActorAssertion]
	c.mu.Unlock()
	if revoked {
		return nil, status.Error(codes.Unauthenticated, "session_revoked")
	}
	var account string
	switch request.ActorAssertion {
	case "alice", "alice-revocable":
		account = "account_alice"
	case "bob":
		account = "account_bob"
	case "wrong-audience", "wrong-method":
		return nil, status.Error(codes.PermissionDenied, "actor_not_authorized")
	case "revoked":
		return nil, status.Error(codes.Unauthenticated, "session_revoked")
	default:
		return nil, status.Error(codes.Unauthenticated, "invalid_actor")
	}
	return &identitypb.VerifyActorResponse{Principal: &identitypb.Principal{AccountId: account, ClientKind: identitypb.ClientKind_CLIENT_KIND_WEB_SESSION}}, nil
}

type assetTestPKI struct {
	dir string
	ca  *x509.Certificate
	key *ecdsa.PrivateKey
}

func newAssetTestPKI(t *testing.T) *assetTestPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "asset integration CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err = os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return &assetTestPKI{dir: dir, ca: ca, key: key}
}
func (p *assetTestPKI) cert(t *testing.T, service string) (string, string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	certURL := fmt.Sprintf("spiffe://human-worth/services/%s", service)
	parsedURL, err := url.Parse(certURL)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: service}, DNSNames: []string{service}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{parsedURL}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := filepath.Join(p.dir, service+fmt.Sprintf("-%d.pem", serial)), filepath.Join(p.dir, service+fmt.Sprintf("-%d-key.pem", serial))
	if err = os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath, filepath.Join(p.dir, "ca.pem")
}
