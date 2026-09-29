package identity

import (
	pb "github.com/KDZZZZZZ/human-worth/backend/gen/humanworth/identity/v1"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/adapter/security"
)

// Shared by the integration and opt-in kind suites; no production facade remains.
type KeyRing = security.KeyRing

var randomToken = security.RandomToken
var digest = security.Digest
var newID = security.NewID
var LoadKeyRing = security.LoadKeyRing

func role(value string) pb.Role {
	if value == "admin" {
		return pb.Role_ROLE_ADMIN
	}
	return pb.Role_ROLE_USER
}
