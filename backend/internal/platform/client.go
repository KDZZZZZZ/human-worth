package platform

import (
	"os"

	"github.com/KDZZZZZZ/human-worth/backend/internal/contract"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/credentials"
	_ "google.golang.org/grpc/health"
)

const AssetUploadChunkBytes = contract.AssetUploadChunkBytes
const AssetUploadMessageBytes = 6 << 20
const AssetCompleteTimeout = contract.AssetCompleteTimeout

// Client 复用服务 mTLS 与轮询连接；写操作不允许 gRPC 自动重试。
func Client(target, service string) (*grpc.ClientConn, error) {
	tls, err := TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), service)
	if err != nil {
		return nil, err
	}
	sendBytes := 2 << 20
	if service == "asset" {
		sendBytes = AssetUploadMessageBytes
	}
	return GRPCClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tls)), grpc.WithDisableRetry(), grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`), grpc.WithStatsHandler(otelgrpc.NewClientHandler()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2<<20), grpc.MaxCallSendMsgSize(sendBytes)))
}

// GRPCClient installs shared request metadata propagation on every client connection.
// Production gRPC connections should use this constructor instead of grpc.NewClient.
func GRPCClient(target string, options ...grpc.DialOption) (*grpc.ClientConn, error) {
	return grpc.NewClient(target, WithRequestIDPropagation(options...)...)
}

// HealthCheckedRoundRobin keeps discovery separate from business readiness.
// Opt in only on edges whose server authorizes Health/Watch for this caller.
func HealthCheckedRoundRobin() grpc.DialOption {
	return grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}],"healthCheckConfig":{"serviceName":""}}`)
}
