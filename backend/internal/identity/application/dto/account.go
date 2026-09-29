package dto

type ChangeAccountInput struct {
	AccountID, Role, State string
	ExpectedVersion        int64
}
