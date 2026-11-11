<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Deployment License

The deployment license is an offline, vendor-signed file that caps what this
platform may host. It is signed with Ed25519; the platform verifies the
signature against its configured public key and never trusts the file's
contents on its own.

## Install a license

Choose the vendor `.json` file (or paste its contents) and install. The file
carries a base64 payload and its detached Ed25519 signature. Installing
replaces the current license immediately; the same file is accepted by the
`nereidlink license` command line, so a deployment can be licensed without a
running console. A license that fails signature verification is rejected and the
current license is kept.

The standard Kubernetes `deploy.sh install|upgrade` command checks license
status after rollout. If no deployment license exists, it signs and installs a
default license using the vendor's offline key; if one already exists, the
command leaves it unchanged. The signed hand-off is stored in mode-0700
`../.deployment/deploy-values/nereidlink/` on the deploy host, with deployment
logs kept separately in mode-0700 `../.deployment/deploy-logs/nereidlink/`.
Sensitive files in both locations are mode 0600.

## Fields

- **Status** — Installed, or Not installed.
- **Customer** — the licensed customer.
- **Tenant limit / Current tenants** — how many tenants may exist. Unlimited
  means no cap.
- **Issued at / Expires at** — the validity window.
- **Features** — the licensed feature set; empty means no feature restriction.

## Read-only degradation

When the license is missing, expired, over the tenant or device cap, or the
rollback clock detects tampering, the deployment degrades to **read-only**
instead of stopping:

- no new tenants and no new devices can be created;
- no new device connections are accepted;
- existing devices keep reporting, and existing data stays readable and
  queryable.

A banner on this page shows when the deployment is currently read-only. Import
a valid license to restore full operation; nothing has to be recreated.