package platform_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KDZZZZZZ/human-worth/backend/internal/asset"
	"github.com/KDZZZZZZ/human-worth/backend/internal/content"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/status"
)

type healthReplica struct {
	*health.Server
	id      string
	checks  atomic.Int32
	watches atomic.Int32
	active  atomic.Int32
}

func (s *healthReplica) Check(ctx context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	s.checks.Add(1)
	_ = grpc.SetHeader(ctx, metadata.Pairs("replica", s.id))
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

// Real TCP/HTTP2 with stable discovery addresses: business RPCs must be gated
// per replica, and all-NOT_SERVING must recover without reconnecting/resolving.
func TestHealthCheckedRoundRobinRecovery(t *testing.T) {
	var replicas []*healthReplica
	var endpoints []resolver.Endpoint
	for _, id := range []string{"a", "b"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		h := &healthReplica{Server: health.NewServer(), id: id}
		h.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
		runtime := platform.NewRuntime("test")
		runtime.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
		server := grpc.NewServer(grpc.ChainStreamInterceptor(runtime.Stream, func(s any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
			h.watches.Add(1)
			h.active.Add(1)
			defer h.active.Add(-1)
			return next(s, stream)
		}))
		healthpb.RegisterHealthServer(server, h)
		go func() { _ = server.Serve(listener) }()
		t.Cleanup(server.Stop)
		replicas = append(replicas, h)
		endpoints = append(endpoints, resolver.Endpoint{Addresses: []resolver.Address{{Addr: listener.Addr().String()}}})
	}
	r := manual.NewBuilderWithScheme("health-test")
	r.InitialState(resolver.State{Endpoints: endpoints})
	conn, err := platform.GRPCClient(r.Scheme()+":///replicas", grpc.WithResolvers(r), grpc.WithTransportCredentials(insecure.NewCredentials()), platform.HealthCheckedRoundRobin(), grpc.WithDisableRetry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := healthpb.NewHealthClient(conn)
	wait := func(label string, f func() bool) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for !f() {
			if time.Now().After(until) {
				t.Fatal(label)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	conn.Connect()
	wait("both health subscriptions did not start", func() bool { return replicas[0].active.Load() == 1 && replicas[1].active.Load() == 1 })
	call := func() (string, error) {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		defer cancel()
		var headers metadata.MD
		_, err := client.Check(ctx, &healthpb.HealthCheckRequest{}, grpc.Header(&headers))
		values := headers.Get("replica")
		if len(values) > 0 {
			return values[0], err
		}
		return "", err
	}
	if _, err = call(); err == nil {
		t.Fatal("business call reached an unhealthy replica")
	}
	if replicas[0].checks.Load()+replicas[1].checks.Load() != 0 {
		t.Fatal("health gate was bypassed")
	}
	replicas[0].SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	wait("first replica did not become usable", func() bool { return conn.GetState() == connectivity.Ready })
	for range 20 {
		id, e := call()
		if e != nil || id != "a" {
			t.Fatalf("unhealthy replica selected: %s %v", id, e)
		}
	}
	replicas[1].SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	seen := map[string]bool{}
	wait("recovered replica did not rejoin round robin", func() bool {
		id, e := call()
		if e == nil {
			seen[id] = true
		}
		return len(seen) == 2
	})
	for _, h := range replicas {
		h.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	}
	wait("all-unhealthy channel remained Ready", func() bool { return conn.GetState() == connectivity.TransientFailure })
	before := replicas[0].checks.Load() + replicas[1].checks.Load()
	if _, err = call(); err == nil {
		t.Fatal("all-unhealthy call succeeded")
	}
	if replicas[0].checks.Load()+replicas[1].checks.Load() != before {
		t.Fatal("unhealthy business call escaped")
	}
	// This crosses the ordinary two-second RPC deadline. A Watch timeout would
	// restart the subscriptions and lose the intended persistent health channel.
	time.Sleep(2200 * time.Millisecond)
	for _, h := range replicas {
		if h.watches.Load() != 1 || h.active.Load() != 1 {
			t.Fatal("health subscription was restarted or lost")
		}
	}
	for _, h := range replicas {
		h.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	}
	wait("all-unhealthy recovery failed", func() bool { _, e := call(); return e == nil })
	// Removing an endpoint and closing the channel must cancel the subscriptions.
	r.UpdateState(resolver.State{Endpoints: endpoints[:1]})
	wait("removed replica leaked Watch", func() bool { return replicas[1].active.Load() == 0 })
	_ = conn.Close()
	wait("closed connection leaked Watch", func() bool { return replicas[0].active.Load() == 0 })
}

type contextStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s contextStream) Context() context.Context { return s.ctx }

func TestHealthWatchAuthorizationAndDeadline(t *testing.T) {
	for _, service := range []string{"gateway", "content", "challenge", "identity", "moderation", "asset", "discovery", "challenge-worker", "voting", ""} {
		ctx := context.Background()
		if service != "" {
			uri, _ := url.Parse(platform.ServiceURIPrefix + service)
			cert := &x509.Certificate{URIs: []*url.URL{uri}}
			ctx = peer.NewContext(ctx, &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}, VerifiedChains: [][]*x509.Certificate{{cert}}}}})
		}
		for _, tc := range []struct {
			name        string
			interceptor grpc.StreamServerInterceptor
			allowed     bool
		}{
			{"asset", asset.AuthorizationStream, service == "gateway" || service == "content"},
			{"content", content.AuthorizationStream, service == "gateway"},
		} {
			t.Run(tc.name+"/"+service, func(t *testing.T) {
				called := false
				err := tc.interceptor(nil, contextStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: healthpb.Health_Watch_FullMethodName}, func(any, grpc.ServerStream) error { called = true; return nil })
				if called != tc.allowed || (tc.allowed && err != nil) || (!tc.allowed && status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unauthenticated) {
					t.Fatalf("authorization: called=%v err=%v", called, err)
				}
			})
		}
	}
	runtime := platform.NewRuntime("test")
	for _, method := range []string{healthpb.Health_Watch_FullMethodName, "/test.Service/OrdinaryStream"} {
		ctx, cancel := context.WithCancel(t.Context())
		err := runtime.Stream(nil, contextStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: method}, func(_ any, s grpc.ServerStream) error {
			_, bounded := s.Context().Deadline()
			if bounded != (method != healthpb.Health_Watch_FullMethodName) {
				t.Fatal("unexpected stream deadline", method)
			}
			cancel()
			if s.Context().Err() != context.Canceled {
				t.Fatal("cancellation was not propagated")
			}
			return nil
		})
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}
