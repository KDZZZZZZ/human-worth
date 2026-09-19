package main

import (
	"context"
	"errors"
	"fmt"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("identity stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	dsn, err := platform.Secret("IDENTITY_DATABASE_URL_FILE")
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
		if identity.Migrate(ctx, db) != nil {
			return errors.New("migration failed; check database access and schema version")
		}
		return nil
	}
	origin, err := platform.Required("PUBLIC_ORIGIN")
	if err != nil {
		return err
	}
	encryption, err := identity.LoadKeyRing(os.Getenv("IDENTITY_ENCRYPTION_KEYS_FILE"))
	if err != nil {
		return err
	}
	signing, err := identity.LoadKeyRing(os.Getenv("IDENTITY_SIGNING_KEYS_FILE"))
	if err != nil {
		return err
	}
	cfg := identity.Config{Origin: origin, Encryption: encryption, Signing: signing}
	if os.Getenv("GOOGLE_OAUTH_CLIENT_ID") != "" {
		secret, err := platform.Secret("GOOGLE_OAUTH_CLIENT_SECRET_FILE")
		if err != nil {
			return err
		}
		callback, err := platform.Required("GOOGLE_OAUTH_REDIRECT_URI")
		if err != nil {
			return err
		}
		version, err := platform.Required("GOOGLE_OAUTH_CONFIG_VERSION")
		if err != nil {
			return err
		}
		cfg.OAuth = identity.NewGoogleOAuth(ctx, os.Getenv("GOOGLE_OAUTH_CLIENT_ID"), secret, callback, version)
	}
	service, err := identity.NewServer(db, cfg)
	if err != nil {
		return err
	}
	tlsConfig, err := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "")
	if err != nil {
		return err
	}
	runtime := platform.NewRuntime("identity")
	shutdown, err := platform.Trace(ctx, "identity")
	if err != nil {
		return errors.New("telemetry initialization failed")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdown(ctx)
	}()
	runtime.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "human_worth_database_connections", Help: "Open connections in this replica's pool"}, func() float64 { return float64(db.Stat().TotalConns()) }))
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)), grpc.ChainUnaryInterceptor(runtime.Unary, identity.Authorization), grpc.StreamInterceptor(identity.AuthorizationStream), grpc.StatsHandler(otelgrpc.NewServerHandler()), grpc.MaxRecvMsgSize(65536), grpc.MaxSendMsgSize(131072), grpc.MaxConcurrentStreams(64))
	pb.RegisterIdentityServiceServer(server, service)
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	probe := runtime.Health(platform.Value("HEALTH_LISTEN", ":8081"), func(ctx context.Context) error { return identity.Ready(ctx, db) })
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
		cleanup := time.NewTicker(time.Minute)
		defer cleanup.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check, cancel := context.WithTimeout(ctx, 2*time.Second)
				state := healthpb.HealthCheckResponse_SERVING
				if identity.Ready(check, db) != nil {
					state = healthpb.HealthCheckResponse_NOT_SERVING
				}
				cancel()
				healthServer.SetServingStatus("", state)
			case <-cleanup.C:
				check, cancel := context.WithTimeout(ctx, 3*time.Second)
				if service.Cleanup(check) != nil {
					runtime.Log.Warn("cleanup_unavailable")
				}
				cancel()
			}
		}
	}()
	runtime.Log.Info("started", "google_login_configured", cfg.OAuth != nil, "revision", platform.BuildVersion())
	select {
	case <-ctx.Done():
	case err = <-failures:
		if errors.Is(err, http.ErrServerClosed) || errors.Is(err, grpc.ErrServerStopped) {
			err = nil
		}
	}
	healthServer.Shutdown()
	platform.StopGRPC(server)
	stop, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()
	probe.Shutdown(stop)
	if err != nil {
		return fmt.Errorf("identity listener stopped")
	}
	return nil
}
