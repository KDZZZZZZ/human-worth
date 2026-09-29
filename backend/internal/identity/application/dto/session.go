package dto

type CurrentSessionInput struct{ ActorAssertion string }
type CurrentSessionResult struct {
	AccountID, DisplayName string
	Role                   int32
	CSRFToken              string
}
type LogoutInput struct{ ActorAssertion string }
