// Package grpc adapts the Identity protobuf contract to protocol-free use cases.
package grpc

import (
	"context"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	pb.UnimplementedIdentityServiceServer
	service *application.Service
}

func NewServer(service *application.Service) *Server { return &Server{service: service} }

func (s *Server) StartGoogleLogin(ctx context.Context, r *pb.StartGoogleLoginRequest) (*pb.StartGoogleLoginResponse, error) {
	result, err := s.service.StartGoogleLogin(ctx, dto.StartLoginInput{PreviousFlowCookie: r.GetPreviousFlowCookie()})
	if err != nil {
		return nil, Error(err)
	}
	return &pb.StartGoogleLoginResponse{AuthorizationUrl: result.AuthorizationURL, FlowCookie: result.FlowCookie, MaxAgeSeconds: result.MaxAgeSeconds}, nil
}
func (s *Server) CompleteGoogleLogin(ctx context.Context, r *pb.CompleteGoogleLoginRequest) (*pb.CompleteGoogleLoginResponse, error) {
	result, err := s.service.CompleteGoogleLogin(ctx, completeLoginInput(r))
	if err != nil {
		return nil, Error(err)
	}
	return &pb.CompleteGoogleLoginResponse{RedirectPath: result.RedirectPath, SessionCookie: result.SessionCookie, MaxAgeSeconds: result.MaxAgeSeconds}, nil
}
func (s *Server) ResolvePrincipal(ctx context.Context, r *pb.ResolvePrincipalRequest) (*pb.ResolvePrincipalResponse, error) {
	result, err := s.service.ResolvePrincipal(ctx, resolveInput(r))
	if err != nil {
		return nil, Error(err)
	}
	return &pb.ResolvePrincipalResponse{Principal: principal(result.Principal), ActorAssertion: result.ActorAssertion}, nil
}

// 【阅读 8】VerifyActor 供业务服务验证网关转交的断言，受众直接取自 mTLS 对端服务名，调用者不能伪选其他受众。
// 例如 content/server.go 的 actor 以 content 身份调用，得到 Principal 后只使用可信 account_id 作为作者。
func (s *Server) VerifyActor(ctx context.Context, r *pb.VerifyActorRequest) (*pb.VerifyActorResponse, error) {
	caller, err := platform.ServiceName(ctx)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	result, err := s.service.VerifyActor(ctx, caller, dto.VerifyActorInput{ActorAssertion: r.GetActorAssertion(), FullMethod: r.GetFullMethod()})
	if err != nil {
		return nil, Error(err)
	}
	return &pb.VerifyActorResponse{Principal: principal(result.Principal)}, nil
}
func (s *Server) GetCurrentSession(ctx context.Context, r *pb.GetCurrentSessionRequest) (*pb.GetCurrentSessionResponse, error) {
	result, err := s.service.GetCurrentSession(ctx, dto.CurrentSessionInput{ActorAssertion: r.GetActorAssertion()})
	if err != nil {
		return nil, Error(err)
	}
	return &pb.GetCurrentSessionResponse{Account: &pb.Account{Id: result.AccountID, DisplayName: result.DisplayName}, Role: pb.Role(result.Role), CsrfToken: result.CSRFToken}, nil
}
func (s *Server) LogoutCurrentSession(ctx context.Context, r *pb.LogoutCurrentSessionRequest) (*pb.LogoutCurrentSessionResponse, error) {
	if err := s.service.LogoutCurrentSession(ctx, dto.LogoutInput{ActorAssertion: r.GetActorAssertion()}); err != nil {
		return nil, Error(err)
	}
	return &pb.LogoutCurrentSessionResponse{}, nil
}
func (s *Server) CreateMcpToken(ctx context.Context, r *pb.CreateMcpTokenRequest) (*pb.CreateMcpTokenResponse, error) {
	result, err := s.service.CreateMCPToken(ctx, dto.CreateMCPTokenInput{ActorAssertion: r.GetActorAssertion(), Name: r.GetName(), CreateRequestID: r.GetCreateRequestId()})
	if err != nil {
		return nil, Error(err)
	}
	return &pb.CreateMcpTokenResponse{Token: result.Token, Credential: mcpToken(result.Credential)}, nil
}
func (s *Server) ListMyMcpTokens(ctx context.Context, r *pb.ListMyMcpTokensRequest) (*pb.ListMyMcpTokensResponse, error) {
	result, err := s.service.ListMyMCPTokens(ctx, dto.ListMCPTokensInput{ActorAssertion: r.GetActorAssertion(), Cursor: r.GetCursor(), Limit: r.GetLimit()})
	if err != nil {
		return nil, Error(err)
	}
	response := &pb.ListMyMcpTokensResponse{NextCursor: result.NextCursor}
	for _, item := range result.Items {
		response.Items = append(response.Items, mcpToken(item))
	}
	return response, nil
}
func (s *Server) RevokeMcpToken(ctx context.Context, r *pb.RevokeMcpTokenRequest) (*pb.RevokeMcpTokenResponse, error) {
	if err := s.service.RevokeMCPToken(ctx, dto.RevokeMCPTokenInput{ActorAssertion: r.GetActorAssertion(), CredentialID: r.GetCredentialId()}); err != nil {
		return nil, Error(err)
	}
	return &pb.RevokeMcpTokenResponse{}, nil
}
