// Package domain contains Identity's rules without persistence or protocol dependencies.
package domain

import "errors"

var (
	ErrInvalidRequest        = errors.New("invalid_request")
	ErrInvalidFlow           = errors.New("invalid_oauth_state")
	ErrUnauthenticated       = errors.New("unauthenticated")
	ErrAccountDisabled       = errors.New("account_disabled")
	ErrRevisionConflict      = errors.New("revision_conflict")
	ErrPurposeForbidden      = errors.New("purpose_forbidden")
	ErrCredentialPurpose     = errors.New("credential_purpose_forbidden")
	ErrAdministratorRequired = errors.New("administrator_required")
	ErrInvalidActor          = errors.New("invalid_actor")
)
