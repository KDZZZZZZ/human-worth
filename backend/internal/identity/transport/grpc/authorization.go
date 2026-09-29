package grpc

import (
	"context"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Only gateway can present raw user credentials. Services can only verify assertions.
// 【阅读 4】Authorization 根据 platform/tls.go 的 ServiceName 检查 mTLS 服务身份。
// 原始用户凭据只能经 gateway 提交；业务服务仅能调用 VerifyActor，且仍须通过 auth.go 的方法与受众校验。
func Authorization(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	caller, err := platform.ServiceName(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if info.FullMethod == pb.IdentityService_VerifyActor_FullMethodName {
		switch caller {
		case "content", "asset", "voting", "moderation", "challenge", "discovery":
		default:
			return nil, status.Error(codes.PermissionDenied, "service_forbidden")
		}
	} else if caller != "gateway" {
		return nil, status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return next(ctx, req)
}

// Health.Watch is streaming too; it must not bypass the unary caller policy.
// 【阅读 5】AuthorizationStream 为流式 RPC 复用同一服务授权规则，防止 Health.Watch 绕过一元拦截器。
func AuthorizationStream(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	_, err := Authorization(stream.Context(), nil, &grpc.UnaryServerInfo{FullMethod: info.FullMethod}, func(context.Context, any) (any, error) { return nil, next(server, stream) })
	return err
}
