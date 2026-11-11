<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device Credentials

The **Credentials** tab manages how a device proves its identity. A device cannot connect until it holds at least one valid credential.

## Credential types

Each type proves possession in a different way, and the type is fixed at creation:

| Type | How possession is proven | Typical use |
| --- | --- | --- |
| `TOKEN` | A random shared secret, compared in constant time. | Simulators and simple scripts. |
| `PASSWORD` | A password stored as an argon2id hash. | Devices that already speak username/password. |
| `JWT` | A signed JSON Web Token; issuer, subject and audience are enforced on every connection. | Fleets that already have an identity provider. |
| `X509` | A client certificate at the mTLS layer; the registered certificate must be exactly the presented leaf. | Production devices with a certificate from your own CA. |

Secrets are **write-only**. A newly created secret is shown exactly once and cannot be recovered afterwards — a stored password is an argon2id hash, so the plaintext is unrecoverable by design. Copy and store it securely at creation time.

## Credential ID

When you add a credential, the **Credential ID** is pre-filled from the device's technical canonical name and the credential type: `<canonical_name>_token`, `_password`, `_cert` or `_jwt` (for example `dev-a3f9k2x7_token`). Switching the type re-derives the ID until you edit the field by hand; after that your value is kept. You can always replace it with any non-empty ID that is unique on the device (it is also globally unique across the platform). For X.509 credentials the ID doubles as the MQTT username and the certificate common name.

## Validity window

Every credential carries an effective time and an optional expiry. It only authenticates when it is `ACTIVE` **and** inside its window **and** the device and tenant are themselves active. Leave the window empty for a credential that is effective immediately and never expires.

A background scanner raises escalating warnings as a credential approaches expiry, tied to a single stable alarm per credential rather than one alarm per threshold.

## Rotation with overlap

Rotation replaces a secret without dropping the device offline:

1. Create the new credential — the old one is still valid.
2. Move the device to the new secret.
3. Disable or delete the old credential.

Because both are valid during the overlap, the device never has to reconnect mid-rotation.

## Actions

- **Enable / Disable** — toggles whether the credential may authenticate. Disabling does not delete it.
- **Rotate** — issue a new secret for an existing credential. Leave the secret field empty to keep the current secret and only change the validity window.
- **Revoke all** — disables **every** credential of this device and ends its live session. Use this to cut off a device you no longer trust.
- **Delete** — removes a single credential.

## Notes

- A revoked or expired credential fails closed: no fallback to another credential is attempted.
- Disabling a credential ends nothing by itself; use **Revoke all** when you also want the live session dropped.
