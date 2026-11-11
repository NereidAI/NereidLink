<!--
 Copyright (C) 2026 YIQISOFT
 SPDX-License-Identifier: Apache-2.0
-->

# NereidLink Product Capability Overview

NereidLink is an IoT pipeline platform for on-premises enterprise
deployment, offering multi-protocol device access, a unified messaging
pipeline, a rule engine, alarm notifications and real-time visualization.
The next-generation server backend and edge have been completely
rewritten around four goals: **real-time, simple, secure, and open**.
The wire protocol is not compatible with previous releases.

This document describes product capabilities organized by platform
domain and is continuously refined alongside the product.

---

## 1. Real-Time Communication Architecture

- **All data changes over WebSocket**: telemetry, presence, attributes,
  device shadow, device domain events, alarms, command progress, connector
  status, secret bindings and OTA progress are all pushed over WebSocket at
  millisecond latency. Polling is not allowed.
- **One connection, multiplexed subscriptions**: a single WebSocket
  connection carries multiple subscriptions with server push only;
  reconnect and session resume are supported.
- **Push isolation**: frames are strictly tenant-isolated and integrated
  with RBAC data scopes — a restricted member's wildcard subscription is
  materialized server-side to the visible device set, so frames never cross
  tenants or exceed authorization.
- **Slow-client protection**: each connection has a bounded non-blocking
  send queue served by a single writer goroutine; half-open or slow peers
  are dropped without affecting other users.
- REST is used only for one-off snapshots, queries and writes; all
  real-time behavior is guaranteed by WebSocket.

## 2. Device Access and Management

- **Four device credential types**:
  - TOKEN: server-generated random secrets with constant-time comparison;
  - PASSWORD: argon2id hashes at rest, backward compatible with legacy
    plaintext verification;
  - JWT: multiple signature algorithms and remote JWKS fetching, with both
    key id and algorithm matching, algorithm-confusion protection and
    fail-closed behavior;
  - X.509: mutual TLS with leaf-certificate fingerprint matching and
    certificate revocation list (CRL) checks.
- **Full credential lifecycle**: validity windows, enable/disable,
  in-place rotation, overlap-window rotation (register the replacement
  before disabling the old one) and expiry scanning with warnings.
- **Device lifecycle**: pre-registration (UNCLAIMED) with single-use
  time-limited claim tokens; transfer between customers/groups with
  continuous identity and time series; non-destructive decommission
  (credentials disabled, not deleted, with a recall window), reactivation
  within the window, and terminal archival after expiry.
- **Simple binding relationships**: gateways and sub-devices have a pure
  containment relationship with a single ownership field; the device list
  automatically classifies gateway / sub-device / standalone device,
  supports filtering by owning gateway and shows the relationship both
  ways.
- **Bulk operations**: CSV/XLSX bulk import (with template download and
  validation preview) and bulk decommission; profiles support bulk import
  as well.
- **Alias and notes**: devices can carry a non-semantic display alias and
  free-form notes; list search matches canonical name or alias.
- Only devices in the proper state can authenticate, receive commands and
  persist telemetry.

## 3. Commands and Remote Control

- **Synchronous results**: simplified command frames support
  request-response waiting for execution results; offline devices fail
  immediately with NOT_CONNECTED — nothing is queued behind a false promise
  of success.
- **Two orthogonal dimensions**: interaction (one-way / request-response)
  and direction (read / write), with independent read/write masks; a denied
  direction is rejected explicitly.
- **Command-catalog whitelist**: commands must be declared in the catalog
  first (commands have side effects, so the catalog is the security
  boundary), with enable switches, timeouts and read/write masks; catalog
  entries can be materialized automatically from profile points.
- **Batch commands**: dispatch by device-group subtree or explicit list
  (up to a thousand targets), claimed per item, with partial failures not
  rolled back and live aggregate progress pushed.
- **Gateway-relayed commands**: commands reach the gateway owning the
  sub-device, gated on the sub-device being attached to that exact
  connection; gateway results use the same correlation, timeout and late-
  correction machinery.
- **Gateway operation catalog**: immediate whole-gateway actions (reboot,
  configuration, etc.) are managed as data-driven descriptors with
  parameter/result schemas, risk levels (read/normal/dangerous) and
  per-tenant disable marks.
- **On-demand probes**: immediate probe tasks targeting gateway-bound
  devices, aggregated per gateway into one frame with per-device results
  collected live.
- **Eventually consistent results**: a late device response can correct a
  stored timeout to succeeded.

## 4. Telemetry and Data Pipelines

- **A unified uplink envelope**: the MQTT, HTTP and CoAP ingresses share
  one SDK and one envelope; transport-independent parsing lives in a single
  place, so all ingresses behave identically.
- **Typed points**: number / boolean / string; numbers run descriptor
  transforms while booleans and strings are carried verbatim. Units come
  only from user-defined descriptors — the platform never guesses units.
- **Protocol-agnostic kernel**: the platform parses no industry-specific
  protocol and carries no business logic; edge gateways and direct devices
  integrate through the generic SDK and wire contract.
- **Ingest rate limiting**: a per-device token bucket in front of the
  messaging backbone drops or rejects overload frames before they enter
  stream processing.
- **Bus compression**: internal messages support optional compression
  (LZ4-class / zstd) with a protocol magic header, hot-switchable and
  compatible with in-flight messages.
- **Data forwarding side path**: forwarding to third-party systems is an
  asynchronous side path with an independent stream and backpressure
  isolation per destination; formats and channels are pluggable.
- **Dead-letter queue (DLQ) and storage hooks**: failed messages can be
  inspected in the DLQ, and the persistence stage supports user-defined
  storage hooks.

## 5. Data Storage and Device State

- **A unified data foundation**: PostgreSQL plus TimescaleDB combines
  entity and time-series data; S3-compatible object storage holds firmware
  packages, imports/exports and cold data.
- **Tiered storage**: Hot / Warm / Cold tiers; only the Cold tier is
  deletable and only via operations. Raw telemetry, continuous aggregates
  and compression policies are maintained by the platform.
- **Retention policies**: per-tenant, per-domain (telemetry / events /
  alarms / audit) retention windows with legal-hold support (automatic
  deletion is suspended while a hold is active); deletion is performed by a
  background enforcer, and shortening a window never triggers immediate
  deletion.
- **Device shadow**: desired/reported merge, a delta set, in-sync drift
  detection and ETag optimistic concurrency control; desired state is
  delivered immediately and re-pushed on connect.
- **Three-scope attributes**: client / server / shared scopes; devices can
  report client attributes while the platform can push server and shared
  attributes.
- **Device domain events**: an append-only ledger and live push of typed,
  severity-rated event payloads, independent of the telemetry pipeline; the
  platform stores and routes events but never branches on their content.

## 6. Rule Engine and Automation

- **Versioned rule chains**: rules are versioned per chain, and a root rule
  chain is provisioned for every tenant.
- **Visual editor**: a node-based rule-chain editor with a node palette and
  configuration panels.
- **Three tiers of scripting**: embedded expressions, embedded JavaScript
  (goja), and external arbitrary-language runners, chosen by complexity.
- The platform itself carries no business logic; all data transformations
  and reactions are expressed through tenant-defined rules.

## 7. Alarms and Notifications

- **Alarm lifecycle**: a complete raise / acknowledge / clear state machine
  with assignee support and comment collaboration.
- **Escalation chains**: ordered delay steps and target roles scheduled from
  alarm start, stopped on acknowledge or clear; fixed-cadence repeated
  reminders are supported and never re-fire after a restart.
- **Multi-channel delivery**: in-app inbox, webhook and SMTP email;
  channels can be deployed and extended independently.
- **Link alarms**: unplanned gateway link-down events open alarms
  automatically and recovery clears them; planned shutdowns never alarm, and
  flapping is detected automatically.
- **Daily link-down digest**: per gateway, outage counts and total downtime,
  delivered once per tenant per day.
- **Platform dead-man fallback**: a channel fully isolated from the tenant
  notification system; consecutive probe failures of dependencies such as
  the database and messaging backbone deliver a platform problem report
  directly.

## 8. Edge Management

- **Handshake negotiation and session resume**: on connection, the edge
  negotiates protocol version and feature set; non-overlapping versions or
  missing required features are rejected. A reconnecting edge may reuse its
  prior session within its validity window, and downlink configuration is
  replayed fully and idempotently.
- **Full connector lifecycle**: the platform pushes connector desired state
  and the edge reports actual state (creating / applied / failed / degraded),
  forming a complete state machine.
- **Two-level configuration**: profile connector templates plus instance
  overrides, deep-merged into an effective spec at dispatch; plugin versions
  are selected automatically on the platform side.
- **Schema validation and drift reconciliation**: edges advertise connector
  config schemas validated before writes; periodic list reconciliation
  identifies missing, stale, drifted and extra instances, with an optional
  strict mode that auto-corrects; instance-level health probes let the edge
  assert connect/disconnect autonomously.
- **Discovery and auto-provisioning**: edges report local candidate devices;
  policies inherit across gateway, tenant and deployment levels, with
  automatic registration or human review.
- **Manifest reconciliation**: edges periodically report the full online
  sub-device list and the platform reconciles in an event-driven way; drift
  produces advisories and lease expiry cascades OFFLINE exactly once; the
  registry is never mutated automatically by reported frames.
- **Link watchdog**: heartbeat liveness, suspect/down grading and flap
  detection, kept off the connection plane.

## 9. Firmware OTA

- **Two firmware sources**: an external URL (the edge downloads itself with
  enforced sha256 verification) or platform-hosted packages (object storage
  behind an authenticated path, so edges need no object-store credentials).
- **Phased state and structured result codes**: forward phases including
  pending, initiated, downloading, verified, updating, rebooting and
  confirmed, with over a dozen result codes (flash full, integrity check
  failed, version mismatch, download no progress, reboot timeout and more)
  for precise diagnosis.
- **Three-source timeline**: an append-only per-device timeline
  distinguishing platform, device and gateway sources; high-frequency
  download progress is pushed but not stored.
- **Two success policies**: success on the device report (default), or
  confirmed after reboot once the device reconnects and reports the target
  version.
- **Canary rollout**: percentage-based or fixed-size batches, advancing only
  after an observation pause.
- **Circuit breakers and watchdogs**: failure-rate or consecutive-failure
  thresholds abort automatically; stall and reboot-confirmation watchdogs
  reclaim outcomes on a schedule.
- **Process control**: pause and resume are supported, and cancellation is
  tiered (not-started targets stop immediately, in-progress updates are not
  interrupted and their results are still collected).
- **Target selection**: an explicit device list or a dynamic
  profile/group/customer selector snapshotted at creation; devices already at
  the target version are skipped automatically.
- **Resumable downloads**: direct devices use a pull model with single-byte
  Range resume; the firmware version is written back to the device record
  after success.
- **Firmware repository**: streaming upload, release notes, mutually
  exclusive stable marking per model and model-compatibility checks.

## 10. Security and Compliance

- **Built-in CA and mTLS**: ECDSA P-256 device certificates are issued
  directly from the console; the verification pool is rebuilt per handshake,
  so CA import or disable takes effect without restart; CA private keys never
  enter the database.
- **Multi-factor authentication**: user TOTP (RFC 6238) with one-time
  recovery codes; login lockout, an arithmetic captcha, session-list
  management and immediate single-session revocation.
- **Step-up for dangerous actions**: a signed short-lived TOTP challenge
  consumed once; machine principals are refused.
- **RBAC model**: built-in roles with fine-grained permission points;
  permissions decide *what* a caller can do while data scopes
  (tenant/customer/group) decide *which rows* they can reach, enforced in
  the store layer for both reads and writes.
- **Secret vault**: tenant secrets are AES-256-GCM encrypted under an
  out-of-database file KEK; plaintext is restored only at the downlink last
  mile, never stored on specs, never put on the internal bus, never returned
  over REST or audit; deletion is reference-protected and forced deletion
  requires a human plus TOTP.
- **Audit hash chain**: the append-only audit log links every entry to the
  previous hash and anchors it with daily seals, enabling online verification
  and tamper detection.
- **Device quarantine incident response**: devices are evicted and rejected
  immediately on credential compromise, traffic floods or firmware anomalies,
  while identity and time-series data are preserved.
- **Dual control for high-risk operations**: operators submit orders from a
  local management port, and the super administrator approves cross-channel
  on the web (with content-hash comparison) before execution.

## 11. Multi-Tenancy and License Metering

- **Automated tenant provisioning**: creating a tenant automatically
  provisions streams, a default profile and a root rule chain; failed tasks
  retry automatically or are handed to operators.
- **Tenant operations**: suspension (new connections and writes are rejected
  immediately) and resumption are supported.
- **OAuth2 service accounts**: client-credentials machine identities with
  overlapping-secret rotation; tokens carry fine-grained permission points
  and cannot create other service accounts.
- **Licensing**: licenses are signed envelopes at deployment or tenant scope;
  expiry or over-capacity enters read-only degradation while already connected
  devices can continue reporting.
- **Clock-rollback protection**: database, NATS KV and local file witnesses;
  excessive rollback triggers read-only mode.
- **Metering**: daily statistics on device counts, active devices, message
  volume, logical bytes and tiered-storage facts for billing and capacity
  planning.

## 12. Observability and Operations

- **Metrics and dashboards**: Prometheus metrics plus Grafana dashboards
  covering infrastructure, REST, ingest limiting, bus codec, commands,
  notifications and WebSocket client counts.
- **Tenant business metrics**: devices total/online/offline (excluding
  decommissioned tombstones), active alarms, in-flight commands, active rule
  chains, credential counts, firmware package counts and long-lived task
  counts.
- **Notification SLO metrics**: delivery success rates and end-to-end latency
  by channel and severity, with p50 / p95 / p99 available.
- **Resource-accounting Top-N**: sampled ingress traffic with byte totals
  never extrapolated; aggregated across six dimensions (tenant, device, IP,
  protocol, node and topic pattern) to answer "who is consuming ingress
  capacity right now".
- **Uniform error envelope**: error responses carry a code, message and trace
  id, with request IDs correlating server logs.
- **Operations tooling**: a platform health self-check, explicit database
  migrations, backup markers with backup scripts and a console statistics
  page.

## 13. Developer Ecosystem and Delivery

- **Device SDK**: a public Go SDK covering every device and gateway topic
  (credentials, telemetry, attributes, commands, shadow, OTA, connectors,
  discovery and probes), with no background loops or policy logic, and with
  examples and integration tests.
- **Adapter SDK**: a public adapter framework with zero internal
  dependencies, reused directly by the built-in MQTT/HTTP ingresses.
- **Device simulators**: MQTT, HTTP and CoAP simulators with configurable
  counts, payloads and templates.
- **Vendor-neutral profile library**: third-party edge reports are ingested
  automatically, and protocol families and point addresses are open and
  editable; same-name drift is never overwritten automatically and is
  resolved manually or through explicit adoption.
- **Device-service catalog**: over a dozen official device services
  (Modbus, OPC-UA, ONVIF, BACnet, S7, LLRP, CoAP and more) maintained as
  globally managed data, so one edit takes effect everywhere; custom services
  can be added and removed.
- **Online documentation**: the wire protocol, integration guides and a
  glossary are fully documented online; the in-app help center offers
  bilingual topics covering every page, with machine-verified cross-references.
- **Interactive API docs**: the OpenAPI contract and Swagger UI are compiled
  into the single binary for CDN-free use in air-gapped environments.
- **Delivery forms**: one codebase, two forms — an all-in-one single binary
  or Kubernetes microservices; the minimum stateful dependencies are only
  PostgreSQL and NATS (object storage optional).
- **Internationalization**: the full UI and documentation are bilingual in
  English and Chinese.

---

## To Be Improved

- [ ] Add quantitative metrics for each domain (latency, throughput, scale
  benchmarks).
- [ ] Add typical deployment architectures and application scenarios.
- [ ] Keep this document in sync with every release.

### Changelog

| Date | Change |
|---|---|
| 2026-10-09 | Initial version: product capability overview reorganized into 13 platform domains |
