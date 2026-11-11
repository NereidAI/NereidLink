<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Command Center

The Command Center tracks commands across every device in the tenant.

## Filters

- Search by **command name** or a partial **device name or alias**.
- State filter: choose one lifecycle state, or **In flight** for every non-terminal state (`PENDING`, `CLAIMED`, `QUEUED`).

## Lifecycle states

- `PENDING` / `QUEUED` — waiting for delivery.
- `CLAIMED` — an ingress has taken responsibility for delivery.
- `DELIVERED` — sent to the connected device.
- `SUCCEEDED` — the device returned a successful response.
- `FAILED` / `TIMEOUT` / `NOT_CONNECTED` / `EXPIRED` — terminal failure reasons.

## Columns

Each row shows the creation time (formatted per the selected console language), device canonical name with its alias shown beneath (linked to its detail page), command name, interaction mode (one-way or request-response), timeliness (realtime or persistent) and current state.

## Batch command form

- **Target** — a device group (select from the group list) or explicit devices; the explicit list is a searchable dropdown populated with device names, not a free-form UUID field.
- **Timeout** — prefilled with a 30-second reference value; override as needed.
