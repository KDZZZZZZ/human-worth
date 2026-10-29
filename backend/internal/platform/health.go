package platform

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type dependencyHealthError struct {
	service, connection, code, health string
}

func (e *dependencyHealthError) Error() string { return fmt.Sprintf("%s unavailable", e.service) }

// CheckHealth retains fail-closed readiness while exposing only safe diagnostics.
func CheckHealth(ctx context.Context, conn *grpc.ClientConn, service string) error {
	response, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil || response.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return &dependencyHealthError{service, conn.GetState().String(), status.Code(err).String(), response.GetStatus().String()}
	}
	return nil
}
