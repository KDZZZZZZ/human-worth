package grpc

import (
	contentpb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/content/v1"
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

// Targets binds wire method names to semantic operations. The domain owns policy.
func Targets() []application.Target {
	return []application.Target{
		{Operation: domain.CreateDraft, Audience: "content", Method: contentpb.ContentService_CreateTaskDraft_FullMethodName},
		{Operation: domain.ReadOwnSubmission, Audience: "content", Method: contentpb.ContentService_GetMyTaskSubmission_FullMethodName},
		{Operation: domain.ListOwnSubmissions, Audience: "content", Method: contentpb.ContentService_ListMySubmissions_FullMethodName},
		{Operation: domain.ReplaceDraft, Audience: "content", Method: contentpb.ContentService_ReplaceTaskDraft_FullMethodName},
		{Operation: domain.CurrentSession, Audience: "identity", Method: pb.IdentityService_GetCurrentSession_FullMethodName},
		{Operation: domain.Logout, Audience: "identity", Method: pb.IdentityService_LogoutCurrentSession_FullMethodName},
		{Operation: domain.CreateMCPToken, Audience: "identity", Method: pb.IdentityService_CreateMcpToken_FullMethodName},
		{Operation: domain.ListMCPTokens, Audience: "identity", Method: pb.IdentityService_ListMyMcpTokens_FullMethodName},
		{Operation: domain.RevokeMCPToken, Audience: "identity", Method: pb.IdentityService_RevokeMcpToken_FullMethodName},
		{Operation: domain.ReadTask, Audience: "content", Method: "/humanworth.content.v1.ContentService/GetTask"},
		{Operation: domain.ListTasks, Audience: "discovery", Method: "/humanworth.discovery.v1.DiscoveryService/ListTasks"},
		{Operation: domain.ViewStatistics, Audience: "voting", Method: "/humanworth.voting.v1.VotingService/ViewTaskStatistics"},
		{Operation: domain.CastVote, Audience: "voting", Method: "/humanworth.voting.v1.VotingService/CastVote"},
		{Operation: domain.StartRun, Audience: "challenge", Method: "/humanworth.challenge.v1.ChallengeService/StartRun"},
	}
}
