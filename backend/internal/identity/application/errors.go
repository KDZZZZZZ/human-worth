package application

import (
	"errors"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
)

type ErrorKind string

const (
	Invalid         ErrorKind = "invalid"
	Unauthenticated ErrorKind = "unauthenticated"
	Forbidden       ErrorKind = "forbidden"
	NotFound        ErrorKind = "not_found"
	AlreadyExists   ErrorKind = "already_exists"
	Aborted         ErrorKind = "aborted"
	Unavailable     ErrorKind = "unavailable"
)

type Error struct {
	Kind   ErrorKind
	Reason string
}

func (e *Error) Error() string                   { return e.Reason }
func Reject(kind ErrorKind, reason string) error { return &Error{Kind: kind, Reason: reason} }

// ErrNotFound is internal to persistence ports; each use case chooses what can be disclosed.
var ErrNotFound = errors.New("record not found")

// 【阅读 12】Failure 保留已分类的业务失败，将未知底层错误统一隐藏为 Identity 不可用。
func Failure(err error) *Error {
	var failure *Error
	if errors.As(err, &failure) {
		return failure
	}
	kind := Invalid
	switch {
	case errors.Is(err, domain.ErrInvalidRequest), errors.Is(err, domain.ErrInvalidFlow):
	case errors.Is(err, domain.ErrUnauthenticated), errors.Is(err, domain.ErrInvalidActor):
		kind = Unauthenticated
	case errors.Is(err, domain.ErrAccountDisabled), errors.Is(err, domain.ErrPurposeForbidden), errors.Is(err, domain.ErrCredentialPurpose), errors.Is(err, domain.ErrAdministratorRequired):
		kind = Forbidden
	case errors.Is(err, domain.ErrRevisionConflict):
		kind = Aborted
	default:
		return &Error{Kind: Unavailable, Reason: "identity_unavailable"}
	}
	return &Error{Kind: kind, Reason: err.Error()}
}
