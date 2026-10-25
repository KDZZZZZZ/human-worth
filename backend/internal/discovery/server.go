package discovery

import (
	"context"
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/discovery/v1"
	identitypb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	votingpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/voting/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/platform"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	"sort"
	"time"
)

type Server struct {
	pb.UnimplementedDiscoveryServiceServer
	Identity identitypb.IdentityServiceClient
	Content  contentpb.ContentServiceClient
	Voting   votingpb.VotingServiceClient
}

func Authorization(ctx context.Context, r any, _ *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	name, e := platform.ServiceName(ctx)
	if e != nil {
		return nil, status.Error(codes.Unauthenticated, "service_identity_required")
	}
	if name != "gateway" {
		return nil, status.Error(codes.PermissionDenied, "service_forbidden")
	}
	return next(ctx, r)
}
func (s *Server) actor(ctx context.Context, assertion, method string) error {
	name, e := platform.ServiceName(ctx)
	if e != nil || name != "gateway" {
		return status.Error(codes.PermissionDenied, "service_forbidden")
	}
	if s.Identity == nil {
		return status.Error(codes.Unavailable, "identity_unavailable")
	}
	v, e := s.Identity.VerifyActor(ctx, &identitypb.VerifyActorRequest{ActorAssertion: assertion, FullMethod: method})
	if e != nil {
		return e
	}
	switch v.GetPrincipal().GetClientKind() {
	case identitypb.ClientKind_CLIENT_KIND_ANONYMOUS, identitypb.ClientKind_CLIENT_KIND_WEB_SESSION, identitypb.ClientKind_CLIENT_KIND_MCP_READ:
		return nil
	}
	return status.Error(codes.PermissionDenied, "credential_purpose_forbidden")
}
func (s *Server) ListTasks(ctx context.Context, r *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	if e := s.actor(ctx, r.GetActorAssertion(), pb.DiscoveryService_ListTasks_FullMethodName); e != nil {
		return nil, e
	}
	v, e := s.Content.ListDiscoverableTasks(ctx, &contentpb.ListDiscoverableTasksRequest{Order: r.Order, Category: r.Category, Limit: r.Limit, Cursor: r.Cursor, Query: r.Query})
	if e != nil {
		return nil, e
	}
	return &pb.ListTasksResponse{Items: v.Items, NextCursor: v.NextCursor}, nil
}
func (s *Server) GetPublicBoard(ctx context.Context, r *pb.GetPublicBoardRequest) (*pb.GetPublicBoardResponse, error) {
	if e := s.actor(ctx, r.GetActorAssertion(), pb.DiscoveryService_GetPublicBoard_FullMethodName); e != nil {
		return nil, e
	}
	out := &pb.GetPublicBoardResponse{Overall: &pb.BoardGroup{}}
	groups := map[string]*pb.CategorySummary{}
	cursor := ""
	// ponytail: exact read-through aggregation for this lab's catalog. Move to a
	// durable read model when scanning the published catalog exceeds the RPC budget.
	for {
		page, e := s.Content.ListDiscoverableTasks(ctx, &contentpb.ListDiscoverableTasksRequest{Limit: 100, Cursor: cursor})
		if e != nil {
			return nil, e
		}
		ids := make([]string, 0, len(page.Items))
		for _, task := range page.Items {
			ids = append(ids, task.Id)
		}
		conclusions, e := s.Voting.GetPublicConclusions(ctx, &votingpb.GetPublicConclusionsRequest{TaskIds: ids})
		if e != nil {
			return nil, e
		}
		for _, task := range page.Items {
			key := "null"
			if task.Category != nil {
				key = "value:" + *task.Category
			}
			if groups[key] == nil {
				groups[key] = &pb.CategorySummary{Category: task.Category, Summary: &pb.BoardGroup{}}
			}
			for _, g := range []*pb.BoardGroup{out.Overall, groups[key].Summary} {
				g.PublishedTasks++
				switch conclusions.Conclusions[task.Id] {
				case "human_leading":
					g.HumanLeadingTasks++
				case "agent_leading":
					g.AgentLeadingTasks++
				default:
					g.UndeterminedTasks++
				}
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	keys := make([]string, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out.Categories = append(out.Categories, groups[k])
	}
	out.ComputedAt = timestamppb.New(time.Now())
	return out, nil
}

func AuthorizationStream(server any, stream grpc.ServerStream, info *grpc.StreamServerInfo, next grpc.StreamHandler) error {
	_, err := Authorization(stream.Context(), nil, &grpc.UnaryServerInfo{FullMethod: info.FullMethod}, func(context.Context, any) (any, error) { return nil, next(server, stream) })
	return err
}
