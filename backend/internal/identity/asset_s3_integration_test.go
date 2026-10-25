//go:build integration

package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	assetpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/KDZZZZZZ/human-worth/backend/internal/voting"
	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Real HTTP, Identity, mTLS gRPC, restricted PostgreSQL roles and external S3.
// Services are in-process servers: this is NOT a kind rollout or OS-process test.
func TestAssetSharedS3HTTPContentIntegration(t *testing.T) {
	cfg := sharedS3TestConfig(t)
	lab := newIdentityLab(t)
	assetDB := serviceTestDatabase(t, lab, "asset", asset.Migrate, "../../../ops/asset-grants.sql")
	contentDB := serviceTestDatabase(t, lab, "content", content.Migrate, "../../../ops/content/grants.sql")
	var assets [2]assetpb.AssetServiceClient
	var contents [2]contentpb.ContentServiceClient
	for i := range assets {
		storage, e := asset.NewS3Storage(cfg)
		must(t, e)
		must(t, asset.CheckStorage(t.Context(), storage))
		assets[i] = assetpb.NewAssetServiceClient(serviceTestGRPC(t, lab, "asset", "gateway", asset.Authorization, func(server *grpc.Server) {
			assetpb.RegisterAssetServiceServer(server, layeredAssetServer(assetDB, storage, lab.client(t, i, "asset", lab.pki), nil))
		}))
		ownership := assetpb.NewAssetServiceClient(serviceTestGRPC(t, lab, "asset", "content", asset.Authorization, func(server *grpc.Server) {
			assetpb.RegisterAssetServiceServer(server, layeredAssetServer(assetDB, storage, lab.client(t, i, "asset", lab.pki), nil))
		}))
		contents[i] = contentpb.NewContentServiceClient(serviceTestGRPC(t, lab, "content", "gateway", content.Authorization, func(server *grpc.Server) {
			contentpb.RegisterContentServiceServer(server, &content.Server{DB: contentDB, Identity: lab.client(t, i, "content", lab.pki), Asset: ownership})
		}))
		handler, e := gateway.New(lab.clients[i], gateway.Options{Origin: lab.origin, Asset: assets[i], Content: contents[i], Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
		must(t, e)
		lab.web[i].Config.Handler = handler
	}
	alice, aliceSession := lab.login(t, "s3-alice", nil)
	bob, bobSession := lab.login(t, "s3-bob", nil)
	headers := map[string]string{"Origin": lab.origin, "X-CSRF-Token": aliceSession.CsrfToken, "Content-Type": "application/json", "Idempotency-Key": "s3-http-upload-key-01"}
	data := bytes.Repeat([]byte("a"), platform.AssetUploadChunkBytes+43)
	digest := sha256.Sum256(data)
	body := fmt.Sprintf(`{"filename":"private.txt","mediaType":"text/plain","expectedBytes":%d,"sha256":"%s"}`, len(data), hex.EncodeToString(digest[:]))
	httpStatus(t, lab.request(t, 0, "POST", "/api/me/assets/upload-sessions", nil, body, headers), 401)
	response := lab.request(t, 0, "POST", "/api/me/assets/upload-sessions", []*http.Cookie{alice}, body, headers)
	httpStatus(t, response, 201)
	var created struct {
		Asset struct {
			ID string `json:"id"`
		} `json:"asset"`
		Session struct {
			ID string `json:"id"`
		} `json:"uploadSession"`
	}
	readJSON(t, response, &created)
	for i, part := range [][]byte{data[:platform.AssetUploadChunkBytes], data[platform.AssetUploadChunkBytes:]} {
		offset := 0
		if i == 1 {
			offset = platform.AssetUploadChunkBytes
		}
		chunkHeaders := map[string]string{"Origin": lab.origin, "X-CSRF-Token": aliceSession.CsrfToken, "Content-Type": "application/octet-stream", "Content-Range": fmt.Sprintf("bytes %d-%d/%d", offset, offset+len(part)-1, len(data))}
		httpStatus(t, lab.request(t, i, "PUT", "/api/me/assets/upload-sessions/"+created.Session.ID+"/content", []*http.Cookie{alice}, string(part), chunkHeaders), 200)
	}
	completeBody := `{"uploadSessionId":"` + created.Session.ID + `"}`
	httpStatus(t, lab.request(t, 1, "POST", "/api/me/assets/"+created.Asset.ID+"/complete", []*http.Cookie{alice}, completeBody, headers), 200)
	httpStatus(t, lab.request(t, 0, "GET", "/api/me/assets/"+created.Asset.ID, []*http.Cookie{alice}, "", nil), 200)
	httpStatus(t, lab.request(t, 1, "GET", "/api/me/assets/"+created.Asset.ID, []*http.Cookie{bob}, "", nil), 404)
	response = lab.request(t, 0, "GET", "/api/me/assets/"+created.Asset.ID+"/content", []*http.Cookie{alice}, "", nil)
	httpStatus(t, response, 200)
	downloaded, e := io.ReadAll(response.Body)
	response.Body.Close()
	must(t, e)
	if !bytes.Equal(downloaded, data) {
		t.Fatal("HTTP download differs from S3 input")
	}
	draftBody := `{"title":"private S3 draft","summary":"","description":"","entries":[{"side":"human","title":"file work","source":"my work","artifacts":[{"kind":"file","assetId":"` + created.Asset.ID + `"}],"permissions":{"cloudUse":false}}]}`
	headers["Idempotency-Key"] = "s3-http-content-key-01"
	response = lab.request(t, 0, "POST", "/api/tasks", []*http.Cookie{alice}, draftBody, headers)
	httpStatus(t, response, 201)
	var draft draftResult
	readJSON(t, response, &draft)
	if draft.State != "draft" || !strings.Contains(string(draft.Content), created.Asset.ID) {
		t.Fatal("asset reference changed visibility or was not persisted")
	}
	response = lab.request(t, 1, "GET", "/api/me/tasks/"+draft.ID, []*http.Cookie{alice}, "", nil)
	httpStatus(t, response, 200)
	readJSON(t, response, &draft)
	if !strings.Contains(string(draft.Content), created.Asset.ID) {
		t.Fatal("second gateway lost private asset reference")
	}
	bobHeaders := map[string]string{"Origin": lab.origin, "X-CSRF-Token": bobSession.CsrfToken, "Content-Type": "application/json", "Idempotency-Key": "s3-http-bob-content1"}
	httpStatus(t, lab.request(t, 1, "POST", "/api/tasks", []*http.Cookie{bob}, draftBody, bobHeaders), 409)
	httpStatus(t, lab.request(t, 1, "GET", "/api/tasks/"+draft.ID, nil, "", nil), 404)
	httpStatus(t, lab.request(t, 0, "POST", "/api/auth/logout", []*http.Cookie{alice}, "", headers), 204)
	httpStatus(t, lab.request(t, 1, "GET", "/api/me/assets/"+created.Asset.ID, []*http.Cookie{alice}, "", nil), 401)
}

// Real S3 fixture reused by public browsing; absence is explicitly INCONCLUSIVE.
func sharedS3TestConfig(t *testing.T) asset.S3Config {
	t.Helper()
	path := os.Getenv("ASSET_S3_TEST_CONFIG_FILE")
	if path == "" {
		t.Skip("INCONCLUSIVE: ASSET_S3_TEST_CONFIG_FILE is not configured")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("S3 test config must be a private readable file")
	}
	raw, err := os.ReadFile(path)
	must(t, err)
	var cfg asset.S3Config
	must(t, json.Unmarshal(raw, &cfg))
	cfg.Bucket = fmt.Sprintf("hw-asset-http-%x", time.Now().UnixNano())
	remote := s3.New(s3.Options{Region: cfg.Region, BaseEndpoint: aws.String(cfg.Endpoint), UsePathStyle: cfg.ForcePathStyle, RetryMaxAttempts: 1, Credentials: awscredentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")})
	_, err = remote.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)})
	must(t, err)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		pages := s3.NewListObjectsV2Paginator(remote, &s3.ListObjectsV2Input{Bucket: aws.String(cfg.Bucket)})
		for pages.HasMorePages() {
			page, e := pages.NextPage(ctx)
			if e != nil {
				t.Errorf("S3 cleanup: %T", e)
				return
			}
			for _, obj := range page.Contents {
				_, e = remote.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(cfg.Bucket), Key: obj.Key})
				if e != nil {
					t.Errorf("S3 cleanup: %T", e)
				}
			}
		}
		_, e := remote.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(cfg.Bucket)})
		if e != nil {
			t.Errorf("S3 bucket cleanup: %T", e)
		}
	})
	return cfg
}

// Reuse the Identity lab's isolated database/PKI, not production schemas.
func serviceTestDatabase(t *testing.T, lab *identityLab, name string, migrate func(context.Context, *pgxpool.Pool) error, grantsPath string) *pgxpool.Pool {
	t.Helper()
	cfg := lab.db.Config().Copy()
	owner, runtime := cfg.ConnConfig.Database+"_"+name+"_owner", cfg.ConnConfig.Database+"_"+name+"_runtime"
	ownerID, runtimeID := pgx.Identifier{owner}.Sanitize(), pgx.Identifier{runtime}.Sanitize()
	password := randomToken()
	_, err := lab.db.Exec(t.Context(), "CREATE ROLE "+ownerID+" LOGIN PASSWORD '"+password+"'; CREATE ROLE "+runtimeID+" LOGIN PASSWORD '"+password+"'; CREATE SCHEMA "+name+" AUTHORIZATION "+ownerID)
	must(t, err)
	t.Cleanup(func() {
		lab.db.Exec(context.Background(), "DROP SCHEMA "+name+" CASCADE; DROP OWNED BY "+runtimeID+"; DROP OWNED BY "+ownerID)
		lab.admin.Exec(context.Background(), "DROP ROLE "+runtimeID+"; DROP ROLE "+ownerID)
	})
	cfg.ConnConfig.User = owner
	cfg.ConnConfig.Password = password
	ownerDB, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	t.Cleanup(ownerDB.Close)
	must(t, migrate(t.Context(), ownerDB))
	must(t, migrate(t.Context(), ownerDB))
	serviceTestGrants(t, lab, name, runtimeID, grantsPath)
	cfg.ConnConfig.User = runtime
	runtimeDB, err := pgxpool.NewWithConfig(t.Context(), cfg)
	must(t, err)
	t.Cleanup(runtimeDB.Close)
	return runtimeDB
}

// Use the checked-in grants, rather than a stale private copy of runtime grants.
func serviceTestGrants(t *testing.T, lab *identityLab, name, runtimeID, grantsPath string) {
	t.Helper()
	sql, err := os.ReadFile(grantsPath)
	must(t, err)
	lines := strings.Split(string(sql), "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "\\") {
			filtered = append(filtered, line)
		}
	}
	grantSQL := strings.ReplaceAll(strings.Join(filtered, "\n"), name+"_runtime", runtimeID)
	_, err = lab.db.Exec(t.Context(), grantSQL)
	must(t, err)
}
func serviceTestGRPC(t *testing.T, lab *identityLab, name, caller string, authorize grpc.UnaryServerInterceptor, register func(*grpc.Server)) *grpc.ClientConn {
	t.Helper()
	cert, key, ca := lab.pki.cert(t, name)
	serverTLS, err := platform.TLS(cert, key, ca, "")
	must(t, err)
	streamAuthorize := func(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
		_, err := authorize(stream.Context(), nil, &grpc.UnaryServerInfo{FullMethod: info.FullMethod}, func(context.Context, any) (any, error) { return nil, next(server, stream) })
		return err
	}
	if name == "voting" {
		streamAuthorize = voting.AuthorizationStream
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)), grpc.UnaryInterceptor(authorize), grpc.StreamInterceptor(streamAuthorize), grpc.MaxRecvMsgSize(platform.AssetUploadMessageBytes))
	register(server)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, err)
	go server.Serve(listener)
	t.Cleanup(server.Stop)
	c, k, a := lab.pki.cert(t, caller)
	tlsConfig, err := platform.TLS(c, k, a, name)
	must(t, err)
	conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithDisableRetry(), grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(platform.AssetUploadMessageBytes)))
	must(t, err)
	t.Cleanup(func() { conn.Close() })
	return conn
}
