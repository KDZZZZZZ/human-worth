package main

import (
	"context"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/gateway"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/credentials"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"log/slog"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(); err != nil {
		slog.Error("gateway stopped", "reason", err.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	origin, err := platform.Required("PUBLIC_ORIGIN")
	if err != nil {
		return err
	}
	tlsConfig, err := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "identity")
	if err != nil {
		return err
	}
	target, err := platform.Required("IDENTITY_TARGET")
	if err != nil {
		return err
	}
	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)), grpc.WithDisableRetry(), grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`), grpc.WithStatsHandler(otelgrpc.NewClientHandler()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(131072), grpc.MaxCallSendMsgSize(65536)))
	if err != nil {
		return errors.New("identity client initialization failed")
	}
	defer conn.Close()
	// Optional for backward-compatible Identity-only deployments; draft routes fail closed.
	var contentClient contentpb.ContentServiceClient
	var contentConn *grpc.ClientConn
	if target := os.Getenv("CONTENT_TARGET"); target != "" {
		config, e := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "content")
		if e != nil {
			return e
		}
		contentConn, e = grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(config)), grpc.WithDisableRetry(), grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`), grpc.WithStatsHandler(otelgrpc.NewClientHandler()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(131072), grpc.MaxCallSendMsgSize(65536)))
		if e != nil {
			return errors.New("content client initialization failed")
		}
		defer contentConn.Close()
		contentClient = contentpb.NewContentServiceClient(contentConn)
	}
	runtime := platform.NewRuntime("gateway")
	shutdown, err := platform.Trace(ctx, "gateway")
	if err != nil {
		return errors.New("telemetry initialization failed")
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		shutdown(ctx)
	}()
	var proxies []netip.Prefix
	if value := os.Getenv("TRUSTED_PROXY_CIDRS"); value != "" {
		for _, text := range strings.Split(value, ",") {
			network, err := netip.ParsePrefix(strings.TrimSpace(text))
			if err != nil {
				return errors.New("invalid trusted proxy CIDR")
			}
			proxies = append(proxies, network)
		}
	}
	ready := func(ctx context.Context) error {
		response, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
		if err != nil {
			return err
		}
		if response.Status != healthpb.HealthCheckResponse_SERVING {
			return errors.New("identity unavailable")
		}
		if contentConn != nil {
			health, e := healthpb.NewHealthClient(contentConn).Check(ctx, &healthpb.HealthCheckRequest{})
			if e != nil || health.GetStatus() != healthpb.HealthCheckResponse_SERVING {
				return errors.New("content unavailable")
			}
		}
		return nil
	}
	handler, err := gateway.New(pb.NewIdentityServiceClient(conn), gateway.Options{Content: contentClient, DeploymentStatusFile: os.Getenv("DEPLOYMENT_STATUS_FILE"), Origin: origin, LogoPath: platform.Value("SITE_LOGO_FILE", "../public/brand/logo.png"), TrustedProxies: proxies, Logger: runtime.Log, Registry: runtime.Registry, Ready: ready})
	if err != nil {
		return err
	}
	server := &http.Server{Addr: platform.Value("HTTP_LISTEN", ":8080"), Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 20 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	probe := runtime.Health(platform.Value("HEALTH_LISTEN", ":8081"), ready)
	failures := make(chan error, 2)
	go func() {
		if os.Getenv("HTTP_TLS_CERT_FILE") != "" {
			failures <- server.ListenAndServeTLS(os.Getenv("HTTP_TLS_CERT_FILE"), os.Getenv("HTTP_TLS_KEY_FILE"))
		} else {
			failures <- server.ListenAndServe()
		}
	}()
	go func() { failures <- probe.ListenAndServe() }()
	runtime.Log.Info("started", "revision", platform.BuildVersion())
	select {
	case <-ctx.Done():
	case err = <-failures:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	stop, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	server.Shutdown(stop)
	probe.Shutdown(stop)
	if err != nil {
		return errors.New("gateway listener stopped")
	}
	return nil
}
