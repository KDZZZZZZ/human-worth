package dto

import "time"

type CreateMCPTokenInput struct{ ActorAssertion, Name, CreateRequestID string }
type MCPToken struct {
	ID, Name, CreateRequestID, State string
	CreatedAt, ExpiresAt             time.Time
	RevokedAt                        *time.Time
}
type CreatedMCPToken struct {
	Token      string
	Credential MCPToken
}
type ListMCPTokensInput struct {
	ActorAssertion, Cursor string
	Limit                  int32
}
type MCPTokenPage struct {
	Items      []MCPToken
	NextCursor string
}
type RevokeMCPTokenInput struct{ ActorAssertion, CredentialID string }
