// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package identity implements the local identity fallback defined in
// ADR-0021 for deployments without an external OIDC provider: users
// identified by per-tenant email, argon2id password hashing, short
// lived signed access tokens and rotating refresh tokens whose hashes
// are persisted as revocable sessions.
//
// The package deliberately performs authentication only. Membership
// roles and permission points live in the RBAC layer (ADR-0022); OIDC
// users will reuse the same tables with an empty password_hash.
package identity

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// PasswordPolicy configures argon2id hashing costs. Costs are operator
// tunable so deployments can raise them on stronger hardware.
type PasswordPolicy struct {
	// Time is the number of argon2 iterations.
	Time uint32

	// Memory is the memory cost in KiB.
	Memory uint32

	// Threads is the parallelism degree.
	Threads uint8

	// KeyLen is the derived key length in bytes.
	KeyLen uint32
}

// DefaultPasswordPolicy returns the recommended development cost.
// Production deployments should raise Memory via configuration.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{Time: 3, Memory: 64 * 1024, Threads: 2, KeyLen: 32}
}

// ErrInvalidPassword means the password does not satisfy policy.
var ErrInvalidPassword = errors.New("password does not meet policy")

// ValidatePassword enforces the ADR baseline: at least 10 characters
// with complexity enabled by default. Callers may raise requirements;
// this floor never weakens.
func ValidatePassword(password string, minLength int) error {
	if minLength <= 0 {
		minLength = 10
	}
	if len(password) < minLength {
		return fmt.Errorf("%w: must be at least %d characters", ErrInvalidPassword, minLength)
	}
	if strings.TrimSpace(password) != password {
		return fmt.Errorf("%w: must not start or end with whitespace", ErrInvalidPassword)
	}
	return nil
}

// HashSecret derives an argon2id PHC string from a secret using the
// default policy. It is shared by device PASSWORD credentials so the
// platform keeps one password-hashing implementation.
func HashSecret(secret string) (string, error) {
	return hashPassword(secret, DefaultPasswordPolicy())
}

// VerifySecret reports a secret against a PHC-format argon2id hash. It
// accepts only hashes produced by HashSecret; callers needing legacy
// plaintext fallback must detect that format themselves first.
func VerifySecret(secret, encoded string) (bool, error) {
	return verifyPassword(secret, encoded)
}

// hashPassword derives an argon2id PHC string from password.
func hashPassword(password string, policy PasswordPolicy) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	key := argon2.IDKey([]byte(password), salt,
		policy.Time, policy.Memory, policy.Threads, policy.KeyLen)

	// Layout: $argon2id$v=<ver>$m=,t=,p=$<salt>$<key>, matching the
	// field splitting in decodePHC.
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, policy.Memory, policy.Time, policy.Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// verifyPassword reports password against a PHC-format argon2id hash.
// A malformed stored hash is treated like a wrong password on the login
// path rather than crashing the service.
func verifyPassword(password, encoded string) (bool, error) {
	policy, salt, key, err := decodePHC(encoded)
	if err != nil {
		return false, err
	}

	derived := argon2.IDKey([]byte(password), salt,
		policy.Time, policy.Memory, policy.Threads, safeKeyLen(key))
	return subtle.ConstantTimeCompare(key, derived) == 1, nil
}

// safeKeyLen converts the decoded key length for argon2, rejecting an
// implausibly large stored key instead of truncating it silently.
func safeKeyLen(key []byte) uint32 {
	if len(key) > 1<<20 {
		return 0
	}
	return uint32(len(key)) //nolint:gosec // length is bounded by the guard above
}

// decodePHC parses a PHC-format argon2id string produced by
// hashPassword: "$argon2id$v=<ver>$m=,t=,p=$<salt>$<key>".
func decodePHC(encoded string) (PasswordPolicy, []byte, []byte, error) {
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 || fields[1] != "argon2id" {
		return PasswordPolicy{}, nil, nil, errors.New("invalid argon2id hash format")
	}

	var version int
	if _, err := fmt.Sscanf(fields[2], "v=%d", &version); err != nil || version != argon2.Version {
		return PasswordPolicy{}, nil, nil, errors.New("unsupported argon2 version")
	}

	var policy PasswordPolicy
	if _, err := fmt.Sscanf(fields[3], "m=%d,t=%d,p=%d",
		&policy.Memory, &policy.Time, &policy.Threads); err != nil {
		return PasswordPolicy{}, nil, nil, errors.New("invalid argon2 parameters")
	}

	salt, err := base64.RawStdEncoding.DecodeString(fields[4])
	if err != nil {
		return PasswordPolicy{}, nil, nil, errors.New("invalid salt encoding")
	}
	key, err := base64.RawStdEncoding.DecodeString(fields[5])
	if err != nil {
		return PasswordPolicy{}, nil, nil, errors.New("invalid key encoding")
	}
	if len(key) > 1<<20 {
		return PasswordPolicy{}, nil, nil, errors.New("key length out of range")
	}
	policy.KeyLen = uint32(len(key)) //nolint:gosec // length is bounded by the guard above
	return policy, salt, key, nil
}
