<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Edge profiles

The **Edge profiles** tab is available on **[gateway](help:glossary/gateway) devices**. It lists the [device profiles](help:glossary/profile) the gateway has reported to NereidLink over MQTT (`v1/gateway/profiles`) and lets you inspect them. NereidLink accepts a vendor-neutral point model as well as the EdgeX profile shape.

## Profiles enter the library automatically

- Every [connector](help:glossary/connector) the edge applies can reference a profile. After the gateway completes its hello handshake (and on each reconnect), it reports those profiles automatically.
- A reported profile is **auto-ingested on first sight** into the tenant profile library (visible on the **Profiles** page, source **Edge**); there is no longer a separate adopt step required before it can be referenced.
- Each row here is the per-gateway raw cache (`name` plus **deviceResource**/point count, payload kept verbatim). The same name from two gateways links to one library profile; provenance is many-to-many.
- Re-reporting an unchanged profile only refreshes the **last reported** time (the platform computes the content hash itself; nothing on the wire is trusted).
- A same-name report whose content changed is **drift**: it is never silently overwritten. Use **Adopt → update** from this tab to accept this gateway's version.

## Searching

Use the search box at the top to filter the list by profile name (case-insensitive, partial match). Filtering is local, so it covers every cached profile without a round trip.

## Viewing profile content

Click **View** on a row to inspect the cached profile. The drawer lists every reported deviceResource in report order with its name, value type, units and description, and marks resources that cannot be represented as a scalar telemetry point (`Binary`/`Object`/`*Array`, duplicate or empty names).

## Adopt / sync

Adoption now resolves this gateway cache row onto the library profile:

- A plain **Adopt** links/confirms the cache row against the auto-ingested library profile (idempotent).
- **Overwrite (update)** accepts this gateway's reported content over a detected drift, refreshing the spec-owned point fields (`type`, `unit`, `read_write`, `address`, range/default). Operator calibration (transform, scale, enum, valid range) is never overwritten.
- Points the edge removes are marked inactive rather than deleted, preserving history and rule references.

## Permissions

Listing and viewing require `profile:read`; adopting requires `profile:write`.
