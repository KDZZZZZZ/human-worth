package platform

import (
	"os"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	_ "google.golang.org/grpc/balancer/roundrobin"
	"google.golang.org/grpc/credentials"
)

// Client 复用服务 mTLS 与轮询连接；写操作不允许 gRPC 自动重试。
func Client(target, service string) (*grpc.ClientConn, error) {
	tls, err := TLS(os.Getenv("SERVICE_CERT_FILE"), os.Getenv("SERVICE_KEY_FILE"), os.Getenv("SERVICE_CA_FILE"), service)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(target, grpc.WithTransportCredentials(credentials.NewTLS(tls)), grpc.WithDisableRetry(), grpc.WithDefaultServiceConfig(`{"loadBalancingConfig":[{"round_robin":{}}]}`), grpc.WithStatsHandler(otelgrpc.NewClientHandler()), grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(2<<20), grpc.MaxCallSendMsgSize(2<<20)))
}
