package platform

import (
	"context"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"time"
)

type Runtime struct {
	Log      *slog.Logger
	Registry *prometheus.Registry
	RPC      *prometheus.HistogramVec
	inflight chan struct{}
}

func NewRuntime(service string) *Runtime {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", service, "instance", Value("POD_NAME", "local"))
	registry := prometheus.NewRegistry()
	registry.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	latency := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "human_worth_rpc_duration_seconds", Help: "Completed RPC duration, including rejected requests", Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 15}}, []string{"method", "code"})
	registry.MustRegister(latency)
	return &Runtime{Log: logger, Registry: registry, RPC: latency, inflight: make(chan struct{}, 64)}
}
func Trace(ctx context.Context, service string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	options := []sdktrace.TracerProviderOption{sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", service))), sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(.1)))}
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" {
		exporter, err := otlptracehttp.New(ctx, otlptracehttp.WithTimeout(2*time.Second))
		if err != nil {
			return nil, err
		}
		options = append(options, sdktrace.WithBatcher(exporter, sdktrace.WithMaxQueueSize(512), sdktrace.WithExportTimeout(2*time.Second)))
	}
	provider := sdktrace.NewTracerProvider(options...)
	otel.SetTracerProvider(provider)
	return provider.Shutdown, nil
}
func (r *Runtime) Unary(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (response any, err error) {
	began := time.Now()
	span := trace.SpanFromContext(ctx)
	requestID := ""
	if values := metadata.ValueFromIncomingContext(ctx, "x-request-id"); len(values) == 1 && len(values[0]) == 22 {
		requestID = values[0]
		for _, c := range requestID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
				requestID = ""
				break
			}
		}
	}
	defer func() {
		if recover() != nil {
			r.Log.Error("rpc_panic", "method", info.FullMethod)
			err = status.Error(codes.Internal, "internal_error")
		}
		code := status.Code(err).String()
		r.RPC.WithLabelValues(info.FullMethod, code).Observe(time.Since(began).Seconds())
		if err != nil {
			span.SetStatus(otelcodes.Error, code)
		}
		r.Log.Info("rpc", "method", info.FullMethod, "code", code, "duration_ms", time.Since(began).Milliseconds(), "request_id", requestID, "trace_id", span.SpanContext().TraceID().String())
	}()
	select {
	case r.inflight <- struct{}{}:
		defer func() { <-r.inflight }()
	default:
		return nil, status.Error(codes.ResourceExhausted, "too_many_requests")
	}
	timeout := 2 * time.Second
	// 批量校验初始材料属于有界慢调用，保留其余 RPC 的短期限。
	if info.FullMethod == "/humanworth.identity.v1.IdentityService/CompleteGoogleLogin" || info.FullMethod == "/humanworth.challenge.v1.ChallengeService/StartRun" || info.FullMethod == "/humanworth.challenge.v1.ChallengeService/RestartRun" {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return next(ctx, request)
}
func (r *Runtime) Health(address string, ready func(context.Context) error) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
		defer cancel()
		if ready(ctx) != nil {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})
	mux.Handle("GET /metrics", promhttp.HandlerFor(r.Registry, promhttp.HandlerOpts{}))
	return &http.Server{Addr: address, Handler: mux, ReadHeaderTimeout: 3 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
}
func StopGRPC(server *grpc.Server) {
	done := make(chan struct{})
	go func() { server.GracefulStop(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		server.Stop()
	}
}

var Revision string

func BuildVersion() string {
	if Revision != "" {
		return Revision
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "development"
	}
	for _, setting := range info.Settings {
		if setting.Key == "vcs.revision" {
			return setting.Value
		}
	}
	return "development"
}
