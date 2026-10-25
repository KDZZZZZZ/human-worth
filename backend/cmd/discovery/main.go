package main

import (
	"context"
	"errors"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/discovery/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/discovery"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	if e := run(); e != nil {
		slog.Error("discovery stopped", "reason", e.Error())
		os.Exit(1)
	}
}
func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	conns := map[string]*grpc.ClientConn{}
	for _, name := range []string{"identity", "content", "voting"} {
		target, e := platform.Required(strings.ToUpper(name) + "_TARGET")
		if e != nil {
			return e
		}
		c, e := platform.Client(target, name)
		if e != nil {
			return e
		}
		defer c.Close()
		c.Connect()
		conns[name] = c
	}
	tls, e := platform.TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), "")
	if e != nil {
		return e
	}
	runtime := platform.NewRuntime("discovery")
	shutdown, e := platform.Trace(ctx, "discovery")
	if e != nil {
		return e
	}
	defer func() { c, done := context.WithTimeout(context.Background(), 3*time.Second); defer done(); shutdown(c) }()
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(tls)), grpc.ChainUnaryInterceptor(runtime.Unary, discovery.Authorization), grpc.StreamInterceptor(discovery.AuthorizationStream), grpc.MaxRecvMsgSize(64<<10), grpc.MaxSendMsgSize(2<<20))
	pb.RegisterDiscoveryServiceServer(server, &discovery.Server{Identity: identitypb.NewIdentityServiceClient(conns["identity"]), Content: contentpb.NewContentServiceClient(conns["content"]), Voting: votingpb.NewVotingServiceClient(conns["voting"])})
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	ready := func(context.Context) error {
		// Ordinary discovery does not require Voting. Keep its endpoints
		// available during a Voting outage; only the board request fails.
		for _, name := range []string{"identity", "content"} {
			if conns[name].GetState() != connectivity.Ready {
				return errors.New("dependency unavailable")
			}
		}
		return nil
	}
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	probe := runtime.Health(platform.Value("HEALTH_LISTEN", ":8081"), ready)
	listener, e := net.Listen("tcp", platform.Value("GRPC_LISTEN", ":8443"))
	if e != nil {
		return e
	}
	failures := make(chan error, 2)
	go func() { failures <- server.Serve(listener) }()
	go func() { failures <- probe.ListenAndServe() }()
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				state := healthpb.HealthCheckResponse_SERVING
				if ready(ctx) != nil {
					state = healthpb.HealthCheckResponse_NOT_SERVING
				}
				healthServer.SetServingStatus("", state)
			}
		}
	}()
	select {
	case <-ctx.Done():
	case e = <-failures:
	}
	cancel()
	healthServer.Shutdown()
	platform.StopGRPC(server)
	stop, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	probe.Shutdown(stop)
	return e
}
