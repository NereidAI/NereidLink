<!--
 Copyright (C) 2026 YIQISOFT
 SPDX-License-Identifier: Apache-2.0
-->

# NereidLink Adapter SDK

The SDK lets developers add a new southbound protocol as a **separate
project or repository**. An adapter translates one wire protocol into
platform contracts; it owns no command state machine and stores no
authoritative state.

## Packages

| Package | Purpose |
|---|---|
| `sdk/adapter` | Lifecycle: config loading, enrollment, heartbeats, presence client, command dispatcher, request-based sessions/mailbox relay |
| `sdk/contract` | Stable wire contract: dispatch/event subject prefixes, command statuses, interaction modes, lifecycle `Event` |
| `sdk/telemetry` | Telemetry uplink: JSON frame → typed points → `Envelope` per point → JetStream partition; stream ensuring; built-in protocol identifiers; binary media markers (`{"blob":true,...,"data":"<base64>"}`) offloaded through an injectable `MediaStore`, producing `blob://` references |
| `sdk/registry` | Registry gRPC client for device authentication |
| `sdk/profile` | Optional device-profile descriptor resolution (PostgreSQL-backed, cached) and raw-to-engineering conversion |
| `sdk/partition` | Immutable partition topology used by the uplink |
| `sdk/scriptruntime` | JavaScript runtime used by expression descriptors |
| `api/gen/nereidlink/v1` | Generated protobuf message types |

The SDK has **no dependency on `internal/`**. Breaking changes to SDK
packages follow semantic versioning; internal packages may change at
any time.

## Adapter lifecycle

1. Load the shared config and connect to NATS (Core + JetStream).
2. Optionally ensure telemetry streams (`telemetry.EnsureStreams`).
3. `adapter.Enroll(...)` with the module type, ingress ID and version.
4. Start presence (`Run`) and subscribe to command dispatch.
5. Translate protocol traffic to `telemetry.Publish` calls; translate
   dispatched commands to protocol writes; reply via the command handler.
   When the protocol carries binary payloads, implement
   `telemetry.MediaStore` against your storage and pass
   `telemetry.WithMediaStore(...)`; frame `data` fields are always base64
   and published points reference the object as `blob://<key>`.
6. On graceful shutdown, disconnect sessions and let heartbeats stop.

A complete annotated skeleton is at `adapters/template`. The built-in
CoAP adapter (`adapters/coap`) is a full reference.

## Starting an external project

```bash
mkdir myadapter && cd myadapter
go mod init github.com/example/myadapter
go get github.com/nereidai/nereidlink@v0.0.1
```

Then copy `adapters/template/main.go` as `main.go`. Only SDK packages
listed above are needed; never import `internal/` from an external
module (Go forbids it).

## Shared configuration file

```yaml
core:
  nats_url: nats://127.0.0.1:4222
  nats_user: module
  nats_password: module-password
  registry_grpc: 127.0.0.1:19090
  registry_token: dev-module-token
  management_url: http://127.0.0.1:8080
  postgres_dsn: ""            # optional; enables descriptor resolution
module:
  enrollment_token: my-enrollment-token
  heartbeat_interval: 10s
```

Protocol-specific configuration is the adapter's responsibility: parse
the same YAML file with an additional wrapper struct.

## Contract invariants

- Arrays in API responses are always `[]`, never `null`.
- Units are never guessed: they only come from user-defined descriptors.
- Presence and the command state machine remain core responsibilities;
  adapters report session boundaries and command acks, nothing more.
- Use a unique subscription identifier per logical subscription so two
  widgets/adapters never replace each other.
