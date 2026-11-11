<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# license-sign

Offline vendor tool that produces a signed NereidLink **license envelope**
accepted by `nereidlink license install`. It is the only component that needs
the Ed25519 signing key; a running server only ever holds the matching public
verification key.

License files are **never baked into an image**. They are signed here, kept by
the operator, and imported at runtime.

## Key model

| Key | Held by | Committed? | Mounted into the server? |
|-----|---------|-----------|--------------------------|
| Ed25519 private key (`vendor-ed25519.pem`) | Vendor, offline | **No** (outside Git) | Never |
| Ed25519 public key (`public.pem`) | Distributed trust anchor | Yes (`configs/license/public.pem`) | Baked in, or mounted as a Secret |

Keep the private key under the central, mode-0700 deployment-values directory.
Generate a key once and protect it without replacing an existing key:

```bash
PRIVATE_KEY=../.deployment/deploy-values/nereidlink/license/private/vendor-ed25519.pem
install -d -m 700 "$(dirname "${PRIVATE_KEY}")"
test ! -e "${PRIVATE_KEY}"
openssl genpkey -algorithm Ed25519 -out "${PRIVATE_KEY}"
chmod 600 "${PRIVATE_KEY}"
openssl pkey -in "${PRIVATE_KEY}" -pubout -out configs/license/public.pem
```

Rotating the signing key means distributing the new `public.pem` and
re-signing all licenses. In Kubernetes you can mount the new public key over
the baked one via `license.existingSecret` without rebuilding the image.

## Usage

```bash
go run ./tools/license-sign \
  --key ../.deployment/deploy-values/nereidlink/license/private/vendor-ed25519.pem \
  --scope '*' \
  --customer 'YIQISOFT Internal' \
  --years 10 \
  --out deployment.license.json
```

### Flags

| Flag | Meaning |
|------|---------|
| `--key` | Path to the Ed25519 PKCS#8 private PEM (required). |
| `--out` | Output path for the signed JSON envelope, mode `0600` (required). |
| `--scope` | `*` for a deployment license, or a tenant UUID for a tenant license. |
| `--customer` | Contract/customer display name. |
| `--device-cap` | Device cap; `0` = unlimited. |
| `--tenant-cap` | Tenant cap on a deployment license; `0` = unlimited. |
| `--issued` | Issue time as RFC3339; defaults to current UTC. |
| `--expires` | Expiry as RFC3339; mutually exclusive with `--years`. |
| `--years` | Convenience validity length in years from issue time. |
| `--features` | Comma-separated purchased feature identifiers. |

One of `--expires` or `--years` is required. The tool validates the payload
(`license.Payload.Validate`) before signing and refuses to write an invalid or
unsigned envelope.

### Tenant license

```bash
go run ./tools/license-sign \
  --key ../.deployment/deploy-values/nereidlink/license/private/vendor-ed25519.pem \
  --scope 00000000-0000-0000-0000-000000000001 \
  --customer 'Acme' --device-cap 1000 --years 3 \
  --out tenant.license.json
```

## Envelope format

The output is JSON with a base64 canonical JSON `payload` and a detached
base64 Ed25519 `signature` over those exact payload bytes (see
`internal/license`). Signature verification therefore never depends on JSON
field ordering:

```json
{
  "payload": "<base64 JSON>",
  "signature": "<base64 Ed25519>"
}
```

## Installing

For Kubernetes, prefer the idempotent standard entry point:

```bash
deploy/kubernetes/scripts/deploy.sh post-deploy
```

It checks `nereidlink license status` and only generates/installs a default
license when none exists, preserving any operator-installed license on reruns.
The signed hand-off stays in `../.deployment/deploy-values/nereidlink/`, and
deployment logs stay separately in `../.deployment/deploy-logs/nereidlink/`.
Both directories are mode 0700 and their sensitive files are mode 0600.

For a manual installation with the same merged configuration as the server:

```bash
nereidlink -c /etc/nereidlink/config.d license status
nereidlink -c /etc/nereidlink/config.d license install \
  --file deployment.license.json
```

A deployment license uses `scope: "*"` and carries `tenant_cap` / a default
device cap; a tenant license is scoped to a tenant UUID and carries that
tenant's device cap. Installation is audited. Expiry or over-cap puts the
scope into read-only mode per ADR-0024.
