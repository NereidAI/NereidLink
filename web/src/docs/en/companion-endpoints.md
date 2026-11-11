<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Companion Endpoints

A companion endpoint registers one external companion service installation — such as a face access-control or meter-reading service — that consumes edge inference facts through the `companion-subscription/v1` contract. NereidLink ships one generic connector for this contract: adding a companion is registration plus conformance, never a platform code change.

## What the connector owns

- The endpoint registration: companion id, target gateway, base URL, contract version and the bearer token used on every request.
- The desired subscription set produced by business orchestration deployment, one row per source device.
- The companion-reported read-back status (PENDING, APPLIED, FAILED) with the monotonic revision and failure reason.

NereidLink stores no companion domain data — no photos, embeddings, galleries, persons or ledgers. Those stay inside the companion service and customer master-data systems.

## Registering an endpoint

Open **Configuration → Companion Endpoints → Register endpoint** and fill in:

- **Companion ID** — the logical companion id used by business assembly nodes (for example `face-service`). Lowercase slug, immutable after creation.
- **Gateway ID** — the target gateway device name. Also immutable; registering the same companion on another gateway creates a separate endpoint.
- **Base URL** — the absolute http(s) root of the companion installation. Use https for any non-loopback deployment.
- **Bearer token** — issued per companion installation. It is write-only: the UI shows only whether a token is stored, never the value. On edit, leave the field empty to keep the stored token, or clear it and save to remove the token.
- **Skip TLS certificate verification** — only for lab https endpoints with an internal CA that the host does not trust; prefer trusting the CA properly.

Re-registering the same companion id and gateway upserts the existing row instead of creating a duplicate.

## Desired subscriptions and read-back

The eye action opens the desired subscription table for one endpoint. Each row shows the source device, pipeline, conclusion device, the last companion-reported status and revision. Business orchestration replaces the full desired set on every deploy: rows absent from the new set are unsubscribed, and undeploying an instance removes exactly the rows it owned.

A deployment never reports success while a row is FAILED. The failure reason uses stable kebab-case codes returned by the companion; fix the companion-side issue and redeploy. A companion that cannot advertise the expected contract version at `GET /api/v1/version` is rejected before any subscription is written.
