package grpc

import (
	"context"
	"strings"

	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/challenge/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var adminMethods = map[string]bool{
	pb.ChallengeService_StartRun_FullMethodName: true, pb.ChallengeService_GetRun_FullMethodName: true,
	pb.ChallengeService_ListRuns_FullMethodName: true, pb.ChallengeService_GetRunSummary_FullMethodName: true,
	pb.ChallengeService_CancelRun_FullMethodName: true, pb.ChallengeService_RestartRun_FullMethodName: true,
	pb.ChallengeService_RegisterCandidate_FullMethodName: true,
}

// Authorization 先检查 mTLS 服务身份；管理员断言还须在业务方法内向 Identity 验证。
func Authorization(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	caller, err := platform.ServiceName(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if adminMethods[info.FullMethod] {
		if caller != "gateway" {
			return nil, status.Error(codes.PermissionDenied, "service_forbidden")
		}
	} else if caller != "challenge-worker" && !(strings.HasPrefix(info.FullMethod, "/grpc.health.") && caller == "gateway") {
		return nil, status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return next(ctx, req)
}
func AuthorizationStream(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	_, err := Authorization(stream.Context(), nil, &grpc.UnaryServerInfo{FullMethod: info.FullMethod}, func(context.Context, any) (any, error) { return nil, next(server, stream) })
	return err
}
