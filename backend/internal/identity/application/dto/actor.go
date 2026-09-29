package dto

type Principal struct {
	AccountID        string
	Role, ClientKind int32
	CredentialID     string
	AuthVersion      int64
}
type ResolvePrincipalInput struct {
	SessionCookie, MCPToken                 *string
	Audience, FullMethod, Origin, CSRFToken string
}
type ResolvePrincipalResult struct {
	Principal      Principal
	ActorAssertion string
}
type VerifyActorInput struct{ ActorAssertion, FullMethod string }
type PrincipalResult struct{ Principal Principal }
