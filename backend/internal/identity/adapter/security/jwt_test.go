package security

import (
	"encoding/base64"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/application"
	"github.com/KDZZZZZZ/human-worth/backend/internal/identity/domain"
	"github.com/golang-jwt/jwt/v5"
	"testing"
	"time"
)

func TestActorWireCompatibility(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	ring := KeyRing{Active: "key1", Keys: map[string]string{"key1": base64.StdEncoding.EncodeToString(key)}}
	tokens, err := NewTokens(ring)
	if err != nil {
		t.Fatal(err)
	}
	target := application.Target{Audience: "content", Method: "/humanworth.content.v1.ContentService/GetTask", Operation: domain.ReadTask}
	now := time.Now()
	// Literal pre-refactor claims/enums are independent of the new domain constants.
	legacy := jwt.MapClaims{"iss": "human-worth.identity", "sub": "acc_legacy", "aud": []string{"content"}, "iat": now.Unix(), "exp": now.Add(30 * time.Second).Unix(), "jti": "legacy-id", "method": target.Method, "credential_id": "cred_legacy", "kind": int32(2), "role": int32(1), "auth_version": int64(1)}
	old := jwt.NewWithClaims(jwt.SigningMethodHS256, legacy)
	old.Header["kid"] = "key1"
	raw, err := old.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	p, err := tokens.Verify(raw, target)
	if err != nil {
		t.Fatal(err)
	}
	if p.AccountID != "acc_legacy" || p.ClientKind != domain.WebSession || p.Role != domain.User {
		t.Fatal("old claims changed meaning")
	}
	newRaw, err := tokens.Sign(p, target)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.Parse(newRaw, func(token *jwt.Token) (any, error) {
		if token.Header["kid"] != "key1" {
			t.Fatal("kid changed")
		}
		return key, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("human-worth.identity"), jwt.WithAudience("content"), jwt.WithExpirationRequired())
	if err != nil {
		t.Fatal(err)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	for _, field := range []string{"sub", "method", "credential_id", "kind", "role", "auth_version"} {
		want := legacy[field]
		switch v := want.(type) {
		case int32:
			want = float64(v)
		case int64:
			want = float64(v)
		}
		if claims[field] != want {
			t.Fatalf("wire field %s changed", field)
		}
	}
	for _, other := range []application.Target{{Audience: "voting", Method: target.Method}, {Audience: "content", Method: "/humanworth.content.v1.ContentService/CreateTaskDraft"}} {
		if _, err = tokens.Verify(raw, other); err == nil {
			t.Fatal("assertion accepted for another purpose")
		}
	}
}
