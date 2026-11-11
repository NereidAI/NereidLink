<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# NereidLink REST Authentication

This document describes the authentication headers accepted by versioned
`/v1` REST routes. Device MQTT and module enrollment credentials have separate
wire documents.

## Interactive bearer tokens

Human console sessions send an OAuth2-style bearer JWT:

```http
Authorization: Bearer <access-token>
```

The gateway validates the token, loads current tenant memberships, maps the
route to an ADR-0022 permission point, and resolves the caller's data scope.

## User API keys

Long-lived external integrations may send a user API key instead:

```http
X-Api-Key: nlk_<52 lowercase unpadded base32 characters>
```

Properties:

- The key authenticates an existing human user, not a machine service account.
- Empty key scopes inherit the owner's current roles and data scope.
- Explicit scopes must be a subset of the owner's current permission points.
  The resulting principal is authorized only through those points.
- Every request performs a database lookup and argon2id verification. No
  positive credential cache is used, so revocation is immediate.
- Unknown and revoked keys return the same generic unauthorized response.
- Repeated failures for one key prefix are locked after 10 failures within 15
  minutes.
- Disabled owners and inactive tenants are rejected.
- The full plaintext key is returned only once during creation or rotation.
  Management responses contain only the non-secret prefix.
- Rotation replaces the credential and lookup prefix. Revoked or expired keys
  cannot be rotated; a replacement expiry must be in the future, and an empty
  rotation body keeps the current expiry.

API-key management itself requires an interactive bearer session. Keys cannot
mint or manage other keys.

## Machine service accounts

ADR-0021 service accounts remain separate. They use OAuth2
`client_credentials` at `POST /v1/auth/token`, receive short-lived JWTs, and
authorize as machine principals through service-account scopes. User API keys
do not replace that internal machine-to-machine mechanism.
