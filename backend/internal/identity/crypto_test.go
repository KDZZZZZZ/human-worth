package identity

import (
	"encoding/base64"
	"testing"
)

func TestEncryptedSecretsBindPurposeAndSurviveKeyOverlap(t *testing.T) {
	k := KeyRing{Active: "old", Keys: map[string]string{"old": base64.StdEncoding.EncodeToString([]byte(randomToken()[:32]))}}
	cipher, err := k.seal("PKCE verifier", "flow-one:verifier")
	if err != nil {
		t.Fatal(err)
	}
	for _, purpose := range []string{"flow-two:verifier", "flow-one:csrf"} {
		if _, err = k.open(cipher, purpose); err == nil {
			t.Fatal("ciphertext accepted for wrong purpose")
		}
	}
	k.Keys["new"] = base64.StdEncoding.EncodeToString([]byte(randomToken()[:32]))
	k.Active = "new"
	plain, err := k.open(cipher, "flow-one:verifier")
	if err != nil || plain != "PKCE verifier" {
		t.Fatal("old encryption key not usable during overlap")
	}
	if _, err = k.open(cipher+"x", "flow-one:verifier"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	delete(k.Keys, "old")
	if _, err = k.open(cipher, "flow-one:verifier"); err == nil {
		t.Fatal("removed encryption key still usable")
	}
}
