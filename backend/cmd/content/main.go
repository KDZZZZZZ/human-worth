package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	if err := run(); err != nil {
		slog.Error("content stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dsn, err := platform.Secret("CONTENT_DATABASE_URL_FILE")
	if err != nil {
		return err
	}
	db, err := platform.Database(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if content.Migrate(ctx, db) != nil {
			return errors.New("content migration failed; check database access and schema version")
		}
		return nil
	}
	target, err := platform.Required("IDENTITY_TARGET")
	if err != nil {
		return err
	}
	clientTLS, err := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "identity")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(clientTLS)), grpc.WithDisableRetry(), grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`), grpc.WithStatsHandler(otelgrpc.NewClientHandler()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(131072), grpc.MaxCallSendMsgSize(65536)))
	if err != nil {
		return errors.New("identity client initialization failed")
	}
	defer conn.Close()
	serverTLS, err := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "")
	if err != nil {
		return err
	}
	runtime := platform.NewRuntime("content")
	shutdown, err := platform.Trace(ctx, "content")
	if err != nil {
		return errors.New("telemetry initialization failed")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdown(ctx)
	}()
	runtime.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "human_worth_database_connections", Help: "Open connections in this replica's pool"}, func() float64 { return float64(db.Stat().TotalConns()) }))
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(serverTLS)), grpc.ChainUnaryInterceptor(runtime.Unary, content.Authorization), grpc.StreamInterceptor(content.AuthorizationStream), grpc.StatsHandler(otelgrpc.NewServerHandler()), grpc.MaxRecvMsgSize(65536), grpc.MaxSendMsgSize(131072), grpc.MaxConcurrentStreams(64))
	pb.RegisterContentServiceServer(server, &content.Server{DB: db, Identity: identitypb.NewIdentityServiceClient(conn)})
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	// Identity restricts health RPCs to gateway. Content uses connection readiness,
	// while every business request performs a real VerifyActor and fails closed.
	ready := func(ctx context.Context) error {
		if err := content.Ready(ctx, db); err != nil {
			return err
		}
		conn.Connect()
		if conn.GetState() != connectivity.Ready {
			return errors.New("identity unavailable")
		}
		return nil
	}
	probe := runtime.Health(platform.Value("HEALTH_LISTEN", ":8081"), ready)
	listener, err := net.Listen("tcp", platform.Value("GRPC_LISTEN", ":8443"))
	if err != nil {
		return errors.New("gRPC listener unavailable")
	}
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
				check, cancel := context.WithTimeout(ctx, 2*time.Second)
				state := healthpb.HealthCheckResponse_SERVING
				if ready(check) != nil {
					state = healthpb.HealthCheckResponse_NOT_SERVING
				}
				cancel()
				healthServer.SetServingStatus("", state)
			}
		}
	}()
	runtime.Log.Info("started", "revision", platform.BuildVersion())
	select {
	case <-ctx.Done():
	case err = <-failures:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
	}
	cancel()
	healthServer.Shutdown()
	platform.StopGRPC(server)
	stop, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	probe.Shutdown(stop)
	if err != nil {
		return errors.New("content listener stopped")
	}
	return nil
}
