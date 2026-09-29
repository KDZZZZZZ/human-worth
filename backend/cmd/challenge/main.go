package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	asset "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/asset/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	content "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identity "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	voting "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/challenge"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := run(); err != nil {
		slog.Error("challenge stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func allow(value string) map[string]bool {
	out := map[string]bool{}
	for _, v := range strings.Split(value, ",") {
		if strings.TrimSpace(v) != "" {
			out[strings.TrimSpace(v)] = true
		}
	}
	return out
}

// run 复用既有 mTLS、数据库与观测能力，定时恢复未完成的登记及关闭待办。
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dsn, err := platform.Secret("CHALLENGE_DATABASE_URL_FILE")
	if err != nil {
		return err
	}
	db, err := platform.Database(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(os.Args) == 2 && os.Args[1] == "migrate" {
		migration, stop := context.WithTimeout(ctx, 30*time.Second)
		defer stop()
		if challenge.Migrate(migration, db) != nil {
			return errors.New("migration failed; check database access and schema version")
		}
		return nil
	}
	clients := map[string]*grpc.ClientConn{}
	for _, service := range []string{"identity", "content", "asset", "voting"} {
		target, err := platform.Required(strings.ToUpper(service) + "_TARGET")
		if err != nil {
			return err
		}
		conn, err := platform.Client(target, service)
		if err != nil {
			return err
		}
		defer conn.Close()
		clients[service] = conn
	}
	models, err := platform.Required("CHALLENGE_MODELS")
	if err != nil {
		return err
	}
	s, err := challenge.NewServer(db, challenge.Config{Models: allow(models), Harnesses: allow(platform.Value("CHALLENGE_HARNESSES", "completion-shell")), Skills: allow(os.Getenv("CHALLENGE_SKILLS"))}, identity.NewIdentityServiceClient(clients["identity"]), content.NewContentServiceClient(clients["content"]), asset.NewAssetServiceClient(clients["asset"]), voting.NewVotingServiceClient(clients["voting"]))
	if err != nil {
		return err
	}
	tls, err := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "")
	if err != nil {
		return err
	}
	runtime := platform.NewRuntime("challenge")
	shutdown, err := platform.Trace(ctx, "challenge")
	if err != nil {
		return errors.New("telemetry initialization failed")
	}
	defer func() {
		stop, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		shutdown(stop)
	}()
	runtime.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "human_worth_database_connections", Help: "Open connections in this replica's pool"}, func() float64 { return float64(db.Stat().TotalConns()) }))
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tls)), grpc.ChainUnaryInterceptor(runtime.Unary, challenge.Authorization), grpc.StreamInterceptor(challenge.AuthorizationStream), grpc.StatsHandler(otelgrpc.NewServerHandler()), grpc.MaxRecvMsgSize(2<<20), grpc.MaxSendMsgSize(2<<20), grpc.MaxConcurrentStreams(64))
	pb.RegisterChallengeServiceServer(server, s)
	healthService := health.NewServer()
	healthpb.RegisterHealthServer(server, healthService)
	ready := func(c context.Context) error { return challenge.Ready(c, db) }
	if err = ready(ctx); err != nil {
		return err
	}
	healthService.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	listener, err := net.Listen("tcp", platform.Value("GRPC_LISTEN", ":8443"))
	if err != nil {
		return errors.New("challenge listener unavailable")
	}
	probe := runtime.Health(platform.Value("HEALTH_LISTEN", ":8081"), ready)
	failures := make(chan error, 2)
	go func() { failures <- server.Serve(listener) }()
	go func() { failures <- probe.ListenAndServe() }()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, finish := context.WithTimeout(ctx, 2*time.Second)
				state := healthpb.HealthCheckResponse_SERVING
				if ready(check) != nil {
					state = healthpb.HealthCheckResponse_NOT_SERVING
				}
				finish()
				healthService.SetServingStatus("", state)
				call, stop := context.WithTimeout(ctx, 10*time.Second)
				if s.Reconcile(call) != nil {
					runtime.Log.Warn("reconcile_pending")
				}
				stop()
			}
		}
	}()
	runtime.Log.Info("started", "revision", platform.BuildVersion())
	select {
	case <-ctx.Done():
	case <-failures:
		err = errors.New("challenge listener stopped")
	}
	healthService.Shutdown()
	platform.StopGRPC(server)
	stop, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	probe.Shutdown(stop)
	return err
}
