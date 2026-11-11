// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package credverify proves possession of a secret presented on a device
// connection, dispatching on the credential mechanism from ADR-0009.
//
// It is deliberately shared between the registry Authenticate RPC and the
// credential cache (internal/credcache): verification rules must stay
// identical whether a credential was just loaded from PostgreSQL or
// replayed from a cache. Adding a mechanism therefore changes one place.
package credverify

import (
	"context"
	"crypto/subtle"
	"strings"

	"github.com/nereidai/nereidlink/internal/identity"
)

// KeyResolver selects a public key for a JWKS-backed token. ResolveKey
// returns the key bound to kid for jwksURI; a false result requires callers
// to fail closed.
type KeyResolver interface {
	ResolveKey(ctx context.Context, jwksURI, kid, alg string) (any, bool)
}

// Record is the stored verification material of one credential. It never
// carries a presented secret; it holds the stored material presented
// secrets are checked against.
type Record struct {
	// Type is the credential mechanism: TOKEN, PASSWORD, JWT or X509.
	Type string
	// Secret is the mechanism-specific stored material (token, argon2
	// PHC, JWT verification key, X509 certificate PEM).
	Secret string
	// Extra carries optional mechanism constraints (e.g. JWT claims).
	Extra []byte
}

// Verify proves that presented (or certFingerprint for X509) demonstrates
// possession of the secret for rec. Malformed presented material resolves
// to false rather than an error, matching wrong-secret semantics.
// certFingerprint is meaningful only for X509; resolver supplies remote
// JWK Sets for jwks_uri JWT credentials and may be nil otherwise.
func Verify(ctx context.Context, rec Record, presented, certFingerprint string,
	resolver KeyResolver) (bool, error) {
	switch rec.Type {
	case "TOKEN":
		// Random secrets are compared in constant time to avoid leaking
		// them through timing side channels.
		return subtle.ConstantTimeCompare(
			[]byte(rec.Secret), []byte(presented)) == 1, nil
	case "PASSWORD":
		if strings.HasPrefix(rec.Secret, "$argon2id$") {
			return identity.VerifySecret(presented, rec.Secret)
		}
		// Rows created before password hashing landed store plaintext;
		// keep them verifiable until the next rotation hashes them.
		return subtle.ConstantTimeCompare(
			[]byte(rec.Secret), []byte(presented)) == 1, nil
	case "JWT":
		return verifyJWT(ctx, presented, rec.Secret, rec.Extra, resolver)
	case "X509":
		// Possession is proven at the mTLS layer; the registered
		// certificate must be exactly the presented leaf.
		return verifyCertificate(rec.Secret, certFingerprint)
	default:
		return false, nil
	}
}
