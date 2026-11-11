// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package userapikey implements long-lived credentials that authenticate
// an existing human user for external integrations. A key is not a machine
// identity: empty scopes inherit the owner's live RBAC roles, while explicit
// scopes narrow the same human principal.
package userapikey

import (
	"errors"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/rbac"
	"github.com/nereidai/nereidlink/internal/secrettoken"
)

const (
	// keyPrefix marks user API keys in headers, logs and stored prefixes.
	keyPrefix = "nlk_"

	// keyEntropyBytes yields a 52-character unpadded base32 suffix.
	keyEntropyBytes = 32
	keySuffixLength = 52

	// storedPrefixLength retains the marker plus eight non-secret base32
	// characters. It supports display and indexed lookup without storing
	// enough material to reconstruct the credential.
	storedPrefixLength = len(keyPrefix) + 8
)

// Sentinel errors map to stable gateway responses. Unknown and revoked
// credentials share ErrInvalidCredentials so callers cannot enumerate keys.
var (
	ErrInvalidArgument    = errors.New("invalid API key argument")
	ErrNotFound           = errors.New("API key not found")
	ErrConflict           = errors.New("API key conflict")
	ErrInvalidCredentials = errors.New("invalid API key credentials") //nolint:gosec
	ErrLocked             = errors.New("API key temporarily locked")
	ErrUserDisabled       = errors.New("API key owner is disabled")
	ErrTenantInactive     = errors.New("API key tenant is inactive")
	ErrScopeNotAllowed    = errors.New("API key scope is not held by owner")
)

// APIKey is the management view. It never contains key_hash or plaintext.
type APIKey struct {
	ID         string     `json:"id"`
	TenantID   string     `json:"tenant_id"`
	UserID     string     `json:"user_id"`
	Name       string     `json:"name"`
	KeyPrefix  string     `json:"key_prefix"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Revoked    bool       `json:"revoked"`
	LastUsedAt string     `json:"last_used_at,omitempty"`
	CreatedAt  string     `json:"created_at"`
	CreatedBy  string     `json:"created_by"`
}

// CreateInput carries a user's self-service key request.
type CreateInput struct {
	Name      string
	Scopes    []string
	ExpiresAt *time.Time
}

// CreateResult includes the one-time plaintext key.
type CreateResult struct {
	Key       APIKey
	Plaintext string
}

// RotateResult includes the replacement one-time plaintext key.
type RotateResult struct {
	Key       APIKey
	Plaintext string
	OldPrefix string
}

// AuthResult is the verified credential handed to the RBAC principal builder.
type AuthResult struct {
	KeyID    string
	TenantID string
	UserID   string
	Scopes   []string
}

// GenerateKey returns a full nlk_-prefixed credential.
func GenerateKey() (string, error) {
	return secrettoken.Random(keyPrefix, keyEntropyBytes)
}

// LookupPrefix validates the wire format and returns the stored prefix.
func LookupPrefix(plaintext string) (string, error) {
	if !strings.HasPrefix(plaintext, keyPrefix) ||
		len(plaintext) != len(keyPrefix)+keySuffixLength {
		return "", ErrInvalidCredentials
	}
	if _, err := secrettoken.Encoding.DecodeString(
		strings.TrimPrefix(plaintext, keyPrefix)); err != nil {
		return "", ErrInvalidCredentials
	}
	return plaintext[:storedPrefixLength], nil
}

// NormalizeScopes trims, de-duplicates and validates requested permission
// points. Empty input means the key inherits the owner's live permissions.
func NormalizeScopes(requested []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(requested))
	for _, raw := range requested {
		scope := strings.TrimSpace(raw)
		if scope == "" {
			continue
		}
		if !rbac.IsKnownPoint(scope) {
			return nil, errors.New("unknown permission point: " + scope)
		}
		if !seen[scope] {
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out, nil
}
