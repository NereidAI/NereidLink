<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Edge Secrets

The **Edge Secrets** page is the tenant vault for the credentials the platform delivers to gateways ([edge secrets](help:glossary/edge-secret)). It has two tabs: **Secrets** (the vault, this page) and **Types** (the [secret type](help:page/edge-secret-types) descriptors). Values are write-only: the list and details show metadata (name, kind, type, timestamps) and never the content. The **Secrets** list can be searched by name or type and is paginated.

## Creating a secret

1. Pick a name — a stable identifier usable in `secret://` references.
2. Pick a type:
   - one of the descriptors listed under the **Types** tab, which renders its typed fields; or
   - **Ad-hoc key/value**, for credentials with no descriptor. Add rows of key/value and mark each row as secret. Saving the ad-hoc form with no rows asks you to add at least one row.
3. Save. The complete document is encrypted (AES-256-GCM under a key held outside the database).

## Rotation and deletion

- Saving again **overwrites** the secret in place; no history is kept.
- Normal deletion is protected: if any gateway still holds a non-removed copy, the delete dialog lists those gateways and blocks the hard delete.
- **Force delete & withdraw** is human-only: start and verify a fresh authenticator (MFA) challenge in the dialog. The platform then withdraws every gateway copy. If a gateway is offline its binding stays **Removing** until it reconnects and reports **Removed**; the vault original is deleted only after the last reference disappears.

## Delivering to a gateway

Open a gateway's detail page and use its **Secrets** tab.

1. **Deliver secret**, then choose a typed-document secret.
2. Optionally add target selectors — open `{kind, value}` pairs the platform never interprets. The integration contract defines the first kind, **mac**: the secret is bound to the device with that MAC, and how the edge applies it is entirely the edge's decision. No selectors means the binding is gateway-wide; a binding may carry several selectors and one target may appear on several bindings.
3. Save. The desired state is pushed immediately when the gateway is online; while offline it stays pending and the full set is re-sent idempotently on the next hello.

Each binding shows a monotonic **revision** and its state:

- **Pending** — recorded, not yet written to the gateway.
- **Dispatching** — pushed on the transport.
- **Applied** — the gateway reported success.
- **Failed** — hover the tag for the gateway's reason.
- **Removing / Removed** — withdrawal flow.

A warning banner reads **Gateway firmware does not support secret delivery** when the gateway's last hello did not negotiate the `secrets` feature. Such bindings can be recorded now (they are not sent) and will be delivered after a firmware upgrade.

**Withdraw** moves the binding through **Removing** to **Removed**: the gateway erases its copy on the next sync. The platform keeps the vault original. The table updates live; no polling is needed.

A binding stuck in Pending/Dispatching raises a warning alarm after five minutes; a continuously failed binding escalates from major to critical after one hour.

## Legacy single-value secrets

Older entries (`kind: single value`, created before typed documents) continue to work, including connector configs that reference them as `secret://<name>`. Editing such an entry keeps the single-value shape.
