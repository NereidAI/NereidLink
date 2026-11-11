<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Integrate with user API keys

User API keys let an external integration call the NereidLink REST API as an
existing human user without storing that user's password.

## Request header

Send the key in the `X-Api-Key` header:

```http
GET /v1/tenants/{tenant}/devices
X-Api-Key: nlk_<52 lowercase unpadded base32 characters>
```

Do not place the key in a URL query parameter. The gateway checks the key on
every request; there is no token exchange and no positive credential cache.

## Permissions

- A key with no explicit scopes inherits the owner's current roles and data
  scope.
- A key with explicit scopes can use only those permission points, and every
  point must be held by the owner at creation time and at each request.
- Disabling the owner or tenant blocks the key immediately.
- Revoking the key blocks it immediately.

## Lifecycle

The full key is displayed once after creation or rotation. The console and API
otherwise expose only a non-secret prefix. Rotation issues a replacement
credential and prefix; revoked or expired keys cannot be rotated. Soft
revocation retains the database row and audit history; hard deletion removes
the row while audit events retain the key id and prefix.

Use interactive login sessions to manage keys. An API key cannot create,
rotate, revoke or delete keys, which prevents a narrowed credential from
widening its own access. Tenant administrators can review, revoke and delete
any user's key in the tenant from the **Users & Roles → API keys** tab; users
create and rotate their own keys on **Account Security**.
