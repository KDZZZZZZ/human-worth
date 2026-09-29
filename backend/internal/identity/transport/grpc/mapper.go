package grpc

import (
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application/dto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func completeLoginInput(r *pb.CompleteGoogleLoginRequest) dto.CompleteLoginInput {
	in := dto.CompleteLoginInput{FlowCookie: r.GetFlowCookie(), State: r.GetState(), PreviousSessionCookie: r.GetPreviousSessionCookie()}
	switch outcome := r.GetOutcome().(type) {
	case *pb.CompleteGoogleLoginRequest_Code:
		in.Code = &outcome.Code
	case *pb.CompleteGoogleLoginRequest_ProviderError:
		in.ProviderError = &outcome.ProviderError
	}
	return in
}

func resolveInput(r *pb.ResolvePrincipalRequest) dto.ResolvePrincipalInput {
	in := dto.ResolvePrincipalInput{Audience: r.GetAudience(), FullMethod: r.GetFullMethod(), Origin: r.GetOrigin(), CSRFToken: r.GetCsrfToken()}
	switch credential := r.GetCredential().(type) {
	case *pb.ResolvePrincipalRequest_SessionCookie:
		in.SessionCookie = &credential.SessionCookie
	case *pb.ResolvePrincipalRequest_McpToken:
		in.MCPToken = &credential.McpToken
	}
	return in
}

func principal(p dto.Principal) *pb.Principal {
	return &pb.Principal{AccountId: p.AccountID, CredentialId: p.CredentialID, ClientKind: pb.ClientKind(p.ClientKind), Role: pb.Role(p.Role), AuthVersion: p.AuthVersion}
}

func mcpToken(t dto.MCPToken) *pb.McpToken {
	state := pb.CredentialState_CREDENTIAL_STATE_UNSPECIFIED
	switch t.State {
	case "active":
		state = pb.CredentialState_CREDENTIAL_STATE_ACTIVE
	case "expired":
		state = pb.CredentialState_CREDENTIAL_STATE_EXPIRED
	case "revoked":
		state = pb.CredentialState_CREDENTIAL_STATE_REVOKED
	}
	result := &pb.McpToken{Id: t.ID, Name: t.Name, CreateRequestId: t.CreateRequestID, State: state, CreatedAt: timestamppb.New(t.CreatedAt), ExpiresAt: timestamppb.New(t.ExpiresAt)}
	if t.RevokedAt != nil {
		result.RevokedAt = timestamppb.New(*t.RevokedAt)
	}
	return result
}
