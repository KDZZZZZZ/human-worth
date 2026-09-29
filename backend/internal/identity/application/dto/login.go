// Package dto defines protocol-independent inputs and outputs for Identity use cases.
package dto

type StartLoginInput struct{ PreviousFlowCookie string }
type StartLoginResult struct {
	AuthorizationURL, FlowCookie string
	MaxAgeSeconds                int32
}
type CompleteLoginInput struct {
	FlowCookie, State, PreviousSessionCookie string
	// Pointers retain oneof presence; exactly one must be non-nil, including for empty values.
	Code, ProviderError *string
}
type CompleteLoginResult struct {
	RedirectPath, SessionCookie string
	MaxAgeSeconds               int32
}
