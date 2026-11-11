// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// auth_credential.go proves possession of the secret presented on a
// device connection, dispatching on the credential mechanism from
// ADR-0009. Verification rules live in internal/credverify so the
// registry RPC and the credential cache cannot drift apart.

package registry

import (
	"context"

	"github.com/nereidai/nereidlink/internal/credverify"
	"github.com/nereidai/nereidlink/internal/store"
)

// verifyCredential reports whether presented proves possession of the
// secret for stored. Malformed presented material resolves to false rather
// than an error, matching the login path's wrong-secret semantics.
// certFingerprint carries the mTLS leaf fingerprint and is meaningful
// only for X509 credentials. resolver supplies remote JWK Sets for JWT
// credentials that pin a jwks_uri and may be nil otherwise.
func verifyCredential(ctx context.Context, stored store.StoredCredential, presented, certFingerprint string, resolver keyResolver) (bool, error) {
	return credverify.Verify(ctx,
		credverify.Record{
			Type:   stored.Type,
			Secret: stored.CredentialsValue,
			Extra:  stored.Extra,
		}, presented, certFingerprint, resolver)
}
