package identity

import (
	"context"
	"crypto/subtle"
	"errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"go.opentelemetry.io/otel"
	"golang.org/x/oauth2"
	"io"
	"net/http"
	"strings"
	"time"
)

const GoogleIssuer = "https://accounts.google.com"

type GoogleOAuth struct {
	config   oauth2.Config
	verifier *oidc.IDTokenVerifier
	client   *http.Client
	issuer   string
	Version  string
}
type googleIdentity struct {
	Subject, Name, Email string
	EmailVerified        bool
}

type boundedTransport struct{ base http.RoundTripper }

func (t boundedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	response.Body = &limitedBody{Reader: io.LimitReader(response.Body, 1<<20), Closer: response.Body}
	return response, nil
}

type limitedBody struct {
	io.Reader
	io.Closer
}

func NewGoogleOAuth(ctx context.Context, clientID, secret, callback, version string) *GoogleOAuth {
	return newOAuth(ctx, clientID, secret, callback, version, GoogleIssuer,
		"https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", "https://www.googleapis.com/oauth2/v3/certs")
}
func newOAuth(ctx context.Context, clientID, secret, callback, version, issuer, authURL, tokenURL, jwksURL string) *GoogleOAuth {
	client := &http.Client{Timeout: 10 * time.Second, Transport: boundedTransport{http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	keySet := oidc.NewRemoteKeySet(oidc.ClientContext(ctx, client), jwksURL)
	return &GoogleOAuth{config: oauth2.Config{ClientID: clientID, ClientSecret: secret, RedirectURL: callback,
		Endpoint: oauth2.Endpoint{AuthURL: authURL, TokenURL: tokenURL, AuthStyle: oauth2.AuthStyleInParams}, Scopes: []string{"openid", "email", "profile"}},
		verifier: oidc.NewVerifier(issuer, keySet, &oidc.Config{ClientID: clientID, SupportedSigningAlgs: []string{oidc.RS256}}), client: client, issuer: issuer, Version: version}
}
func (o *GoogleOAuth) authorizationURL(state, nonce, verifier string) string {
	return o.config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}
func (o *GoogleOAuth) exchange(ctx context.Context, code, verifier string, nonceHash []byte) (googleIdentity, error) {
	ctx, span := otel.Tracer("human-worth.identity").Start(ctx, "google.exchange_and_verify")
	defer span.End()
	token, err := o.config.Exchange(oidc.ClientContext(ctx, o.client), code, oauth2.VerifierOption(verifier))
	if err != nil {
		var rejected *oauth2.RetrieveError
		if errors.As(err, &rejected) && rejected.Response != nil && rejected.Response.StatusCode >= 400 && rejected.Response.StatusCode < 500 && rejected.Response.StatusCode != 429 {
			return googleIdentity{}, faultUnauth("invalid_google_login")
		}
		return googleIdentity{}, faultUnavailable("login_unavailable")
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok {
		return googleIdentity{}, faultUnauth("invalid_google_login")
	}
	id, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		// go-oidc v3.21 wraps KeySet errors as text rather than preserving their type.
		if ctx.Err() != nil || strings.HasPrefix(err.Error(), "failed to verify signature: fetching keys ") {
			return googleIdentity{}, faultUnavailable("login_unavailable")
		}
		return googleIdentity{}, faultUnauth("invalid_google_login")
	}
	if id.Subject == "" || len(id.Subject) > 255 || id.Nonce == "" || subtle.ConstantTimeCompare(digest(id.Nonce), nonceHash) != 1 {
		return googleIdentity{}, faultUnauth("invalid_google_login")
	}
	var claims struct {
		Name          string `json:"name"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		AZP           string `json:"azp"`
	}
	if id.Claims(&claims) != nil || (len(id.Audience) > 1 && claims.AZP == "") || (claims.AZP != "" && claims.AZP != o.config.ClientID) {
		return googleIdentity{}, faultUnauth("invalid_google_login")
	}
	name := strings.TrimSpace(claims.Name)
	if name == "" {
		name = "Human Worth 用户"
	}
	if len([]rune(name)) > 100 {
		name = string([]rune(name)[:100])
	}
	if len(claims.Email) > 320 {
		claims.Email = ""
		claims.EmailVerified = false
	}
	return googleIdentity{Subject: id.Subject, Name: name, Email: claims.Email, EmailVerified: claims.EmailVerified}, nil
}
