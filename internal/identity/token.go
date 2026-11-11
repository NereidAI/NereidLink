// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// token.go issues and verifies the compact HS256 access tokens used by
// the local identity fallback. The JWT is hand-rolled rather than
// pulling a third-party library: the only operations needed are
// canonical JSON, base64url and HMAC, all from the standard library.
// OIDC deployments validate IdP JWKS instead and do not use this code.

package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidToken means a token is malformed, expired or has a bad
// signature.
var ErrInvalidToken = errors.New("invalid access token")

// Claims is the access-token payload.
type Claims struct {
	// Subject is the user identifier.
	Subject string `json:"sub"`

	// TenantID is the tenant the user belongs to; empty for a
	// platform-only admin.
	TenantID string `json:"tid,omitempty"`

	// Email is the user identifier echoed for display/auditing.
	Email string `json:"email"`

	// PlatformAdmin marks a cross-tenant platform administrator.
	PlatformAdmin bool `json:"adm,omitempty"`

	// SessionID ties the access token to its revocable refresh session.
	SessionID string `json:"sid,omitempty"`

	// Purpose distinguishes non-session tokens, notably an MFA challenge,
	// from a normal access token. A token issued to prove a password during
	// login must not be accepted as a bearer token merely because it has a
	// valid signature.
	Purpose string `json:"pur,omitempty"`

	// Machine marks an OAuth2 client_credentials token (ADR-0021). A
	// machine principal has no refresh session: it authenticates a service
	// account, not a human, and therefore must never pass a session lookup
	// nor be offered refresh/rotation.
	Machine bool `json:"mt,omitempty"`

	// Scopes are the permission points a machine token may exercise in
	// its tenant. They are empty on human tokens, which authorize through
	// membership roles instead.
	Scopes []string `json:"scope,omitempty"`

	// IssuedAt is the issuance unix timestamp.
	IssuedAt int64 `json:"iat"`

	// ExpiresAt is the expiry unix timestamp.
	ExpiresAt int64 `json:"exp"`
}

// signer issues and verifies tokens with one shared secret. The secret
// is operator-provided configuration; rotating it invalidates every
// outstanding access token (refresh sessions survive and re-issue).
type signer struct {
	secret []byte
}

// newSigner constructs a token signer.
func newSigner(secret string) (*signer, error) {
	if len(secret) < 16 {
		return nil, errors.New("identity jwt secret must be at least 16 characters")
	}
	return &signer{secret: []byte(secret)}, nil
}

// issue serializes and signs a token from claims.
func (s *signer) issue(claims Claims) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	body := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	return body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// parse verifies signature and expiry and returns the claims.
func (s *signer) parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}

	body := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(body))
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(signature, mac.Sum(nil)) {
		return Claims{}, ErrInvalidToken
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if time.Now().Unix() >= claims.ExpiresAt {
		return Claims{}, fmt.Errorf("%w: token expired", ErrInvalidToken)
	}
	return claims, nil
}
