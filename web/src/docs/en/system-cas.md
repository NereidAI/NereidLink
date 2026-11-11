<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device CAs (Platform)

The **Device CAs** page is the platform trust-anchor console for device mTLS. It manages which certificate authorities the platform trusts when validating device client certificates. Only platform administrators can use it; the TLS handshake pool picks up changes live, without a restart.

## What a trust anchor is

A trust anchor is a CA certificate the platform accepts when a device presents an X.509 client certificate. For a device certificate to validate, its chain must lead back to one of the **enabled** anchors listed here. This is the platform-wide trust root for device mTLS — separate from any certificate a tenant issues.

## Built-in vs imported

- **Built-in** — the CA created with the platform installation (`nereidlink pki init`). It cannot be deleted, because every device certificate issued by it would stop validating.
- **Imported** — third-party CAs you add so devices carrying certificates from your own CA can connect.

## Columns

Each anchor shows its **Subject**, **Issuer**, SHA-256 **Fingerprint**, trust **Status** and **Expires** date. The expiry cell also carries a validity tag:

- **Valid** — more than 30 days remain.
- **Expires in Nd** (warning) — the anchor expires within 30 days.
- **Expired** (error) — the certificate is past its `NotAfter`.

Expiry is parsed from the stored certificate, so it reflects the CA itself regardless of whether the anchor is currently enabled or disabled.

## Actions

- **Import** — upload a third-party CA. Provide a label and paste the PEM certificate.
- **Enable / Disable** — toggle whether the platform trusts this anchor. Disabling takes effect immediately: new device handshakes signed by that CA will fail.
- **Download** — export the PEM certificate of an anchor.
- **Delete** — remove an imported anchor. Built-in anchors cannot be deleted.

## Notes

- Changes are read live by the TLS handshake pool; no restart is required.
- The device-facing CA bundle is served from an unauthenticated endpoint so that provisioning tools and device bootstrap code can fetch the public certificates without a login.
- Disabling an anchor can lock out devices whose certificates chain to it — verify before disabling.
