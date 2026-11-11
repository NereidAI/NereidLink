<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Gateway Integration

This guide explains how to integrate a **[gateway](help:glossary/gateway) / edge node** — one process
that connects to NereidLink over a single MQTT connection and manages many
**[sub-devices](help:glossary/subdevice)** behind it (Modbus/ONVIF/S7 field buses,
local device services, and so on). It is written for gateway developers.

- **Transport:** MQTT 3.1.1 / 5.0 over TLS (mTLS optional).
- **Payloads:** JSON, UTF-8.
- **Official SDK:** Go — `github.com/nereidai/nereidlink/sdk/device/go`,
  package `gateway`. A complete reference program is in
  `examples/gateway`. Other languages can implement the wire contract in
  this document directly.
- The platform does not understand any south-side protocol. Mapping between
  platform sub-device UUIDs and local devices is entirely the gateway's job.

## 1. Prerequisites on the platform

1. The gateway itself is registered as a device of type **gateway**, with its
   own credential (ID + secret, token, or X.509 certificate).
2. Each sub-device is registered in the console and **bound to this
   gateway**. A gateway can only operate sub-devices bound to it; frames for
   a foreign or un-attached sub-device are dropped.
3. Sub-device profiles declare the telemetry point names; commands must be
   declared in the per-device command catalog before they can be dispatched.

## 2. Session lifecycle

State machine: `UNIDENTIFIED → NEGOTIATED → CLOSED`.

1. Establish the MQTT connection; pre-register the LWT on
   `v1/gateway/goodbye`.
2. Subscribe the hello-ack topic (or use the MQTT 5 Response Topic).
3. Publish `v1/gateway/hello`; negotiation must finish within 10 seconds.
4. Only after a successful ack may gateway data topics be used. Frames sent
   before the ack are discarded. Exactly one hello happens per connection.

### Hello (gateway → platform), topic `v1/gateway/hello`

```json
{
  "v": 1,
  "edge_id": "edge-001",
  "agent_version": "2.3.1",
  "proto": {"min": "1.0", "max": "1.4"},
  "features": {
    "required": ["telemetry", "presence"],
    "optional": ["ota", "shadow", "blob-relay", "command", "secrets", "profiles"]
  },
  "profile_formats": ["edgex-device-profile/3", "nereid-neutral/1"],
  "connectors": [
    {"type": "modbus", "type_version": "2.1",
     "config_schema": {"type": "object", "required": ["host"],
       "properties": {"host": {"type": "string"}, "port": {"type": "integer"}}}}
  ],
  "client_time": 1727360000123
}
```

- `features.required` must all be supported by the server or negotiation
  fails (`incompatible_features`); `features.optional` are downgraded away
  when unsupported. Available features: `telemetry`, `presence`, `ota`,
  `shadow`, `blob-relay`, `command`, `secrets`, `profiles`.
- `profile_formats` (optional) lists, in preference order, the device-profile
  document formats you can apply (see §10.1); the ack echoes the negotiated
  intersection in `profile_formats`. Omitted or empty means the neutral
  format only.
- `connectors[]` optionally registers inline, self-contained JSON Schemas
  (draft 2020-12; no external `$ref`) for the connector types this gateway
  can run. An invalid schema never fails the handshake; its result is
  reported per entry in the ack.
- `agent_version` is your build string and is independent of the protocol
  version.

### Ack (platform → gateway), topic `v1/gateway/hello/ack`

```json
{
  "session_id": "s_...",
  "status": "ok",
  "selected": "1.4",
  "server": {"min": "1.0", "max": "2.0", "version": "nereidlink-..."},
  "features": ["telemetry", "presence", "command"],
  "profile_formats": ["edgex-device-profile/3"],
  "limits": {"heartbeat_s": 30, "hello_timeout_s": 10,
             "upload": {"max_mb": 256, "qps": 20}},
  "time": {"server_time": 1727360000456, "offset_ms": -312, "clock_warning": false}
}
```

Ack statuses: `ok`, `auth_failed`, `incompatible_version`,
`incompatible_features`, `quota_exceeded`. Use `limits.heartbeat_s` for the
heartbeat cadence. After every reconnect the server state resets to
`UNIDENTIFIED`: re-run hello, re-subscribe, and re-announce state.

## 3. Liveness: heartbeat, goodbye and LWT

- Publish a heartbeat to `v1/gateway/heartbeat` at the negotiated interval:

```json
{"seq": 42, "client_time": 1727360060000}
```

- Pre-register `v1/gateway/goodbye` as the MQTT will at CONNECT time. On
  graceful shutdown publish it explicitly with `planned: true`:

```json
{"reason": "upgrade", "planned": true}
```

  The broker-delivered LWT has no `planned` flag, so a crash is
  distinguishable from planned downtime.
- Missed heartbeats drive the platform watchdog (`HEALTHY → SUSPECT → DOWN`)
  and cascade attached sub-devices offline. A goodbye (planned or LWT)
  suppresses the suspicion timer.

### 3.1 Link episode reports (`v1/gateway/linkhealth`)

Run a local link watchdog in the gateway and report each unplanned
down/up episode after re-hello succeeds (QoS1):

```json
{
  "edge_id": "<gateway-uuid>",
  "session_id": "s_…",
  "down_at": "2026-10-10T03:12:44Z",
  "up_at": "2026-10-10T03:12:49Z",
  "reason": "transport_down"
}
```

- `edge_id` must equal this connection's gateway id; `down_at`/`up_at` are
  RFC3339 (the edge clock is the ground truth) and `up_at` must not precede
  `down_at`; `session_id` is optional.
- The platform corrects the matching lease episode or records a closed
  alarm-free episode for flaps shorter than the lease window, and answers on
  `v1/gateway/linkhealth/ack`: `accepted` / `duplicate` / `rejected`. Stop
  retrying on any of the three; no ack (transient server error) means retry.
- Backlog replay is just oldest-first QoS1 frames; de-duplication is by
  `(edge_id, down_at)`. Five unmatched episodes in 10 minutes raise a
  flapping alarm, auto-cleared after a quiet 10-minute window.

## 4. Sub-device [presence](help:glossary/presence)

| Topic | Direction | Payload |
|---|---|---|
| `v1/gateway/connect` | publish | `{"devices": ["<uuid>", ...]}` |
| `v1/gateway/disconnect` | publish | `{"devices": ["<uuid>", ...]}` |
| `v1/gateway/manifest` | publish | Full-state reconciliation, every 5 minutes |

Announce sub-devices in batches when they attach/detach. The manifest is the
authoritative periodic full list; the platform reconciles desired vs
reported presence from it:

```json
{
  "devices": ["<uuid-1>", "<uuid-2>"],
  "connectors": [
    {"spec_id": "ci-...", "revision": 3, "config_hash": "..."}
  ],
  "missed_cycles": 0
}
```

- `connectors` semantics matter: **omit the field** to skip connector
  reconciliation; send an explicit empty array `[]` to report "zero
  connectors applied".
- Include all currently attached sub-device UUIDs every time; the manifest
  is full-state, not a delta.

## 5. Sub-device [telemetry](help:glossary/telemetry)

Publish batches to `v1/gateway/telemetry`. The payload is a map keyed by
sub-device UUID; each value is the same flat scalar frame as a direct device
reports, with an optional `ts` (epoch milliseconds):

```json
{
  "07704cf9-...": {"ts": 1728374400000, "MACAddress": "AA:BB:CC:00:11:22"},
  "089fbb69-...": {"temperature": 23.1, "online": true}
}
```

Rules are identical to direct devices: values must be numbers/booleans/
strings (no nested objects or arrays); one storage record per key; unknown
keys are stored without descriptor conversion. A value may also be a
`{"blob":true,...}` marker (see direct-device integration): the media is
stored under `media/<tenant>/<sub-device>/...` and the point becomes a
`blob://` reference.

CLIENT-scope attributes use `v1/gateway/attributes` with
`{device: {key: value}}`; SERVER/SHARED pushes arrive on
`v1/gateway/attributes/push` as `{"device":"...","scope":"...","data":{...}}`.
After every (re)connect the gateway should publish
`v1/gateway/attributes/get` (`{"device":"...","scopes":[...],"response_topic"?}`,
omitted `scopes` means SERVER_SCOPE+SHARED_SCOPE) for each attached sub-device
and replace its cache from the full per-scope set in
`v1/gateway/attributes/get/ack`. A JSON `null` value in a push `data` map means
that key was deleted; drop it locally (do not store null). In the current
version both SERVER_SCOPE and SHARED_SCOPE are delivered to the device (unlike
classic ThingsBoard, where SERVER_SCOPE is server-private); do not assume
SERVER_SCOPE is private. The gateway's **own** attributes use the direct-device
topics `v1/me/attributes`, `v1/me/attributes/down`, `v1/me/attributes/get` and
`v1/me/attributes/get/ack` — the gateway is itself a device, and whole-gateway
runtime knobs (for example log level or trace switches) are delivered there,
not under `v1/gateway/...`.

## 6. Commands to sub-devices

Commands are declared per sub-device in the platform command catalog; the
gateway merely executes them against its local bus.

### Downlink (platform → gateway), topic `v1/gateway/command`

One subscription serves every sub-device; the `device` field selects the
target:

```json
{
  "command_id": "01J8...",
  "device": "07704cf9-...",
  "interaction": "request-response",
  "timeliness": "realtime",
  "method": "DeviceInformation",
  "direction": "read",
  "params": {}
}
```

- `interaction`: `one-way` (no reply expected) or `request-response`
  (exactly one correlated reply).
- `timeliness`: `realtime` (fails fast when offline) or `persistent`
  (queued server-side; the gateway only ever sees live frames).
- `method` is the catalog command name; `params` is the catalog-validated
  JSON delivered verbatim.
- `direction`: `read` or `write`, the south-side operation (read a resource
  vs. write a resource), orthogonal to `interaction`. The gateway MUST
  perform the GET/write exactly as directed and MUST NOT infer direction
  from the method name; a read may carry `params` (query arguments).
  `one-way` frames never carry `direction=read`.

### Uplink (gateway → platform), topic `v1/gateway/command/response`

```json
{
  "command_id": "01J8...",
  "device": "07704cf9-...",
  "data": {"manufacturer": "Acme", "model": "Cam-9", "firmware": "1.4.2"}
}
```

- Echo `command_id` and `device` unchanged; `data` is arbitrary JSON.
- For `one-way` commands send nothing.
- On local failure (device not reachable on the field bus, read error,
  timeout), still answer request-response commands with the error inside
  `data`, for example `{"error": "no response from field bus"}`. Not
  answering at all causes a platform-side `TIMEOUT`.
- A response is accepted only from the gateway that attached the named
  sub-device.

Go SDK:

```go
_ = gw.SubscribeCommands(ctx, 1, func(cmd device.GatewayCommand) {
    if cmd.Interaction != device.InteractionRequestResponse {
        return // one-way: execute silently
    }
    result := executeOnFieldBus(cmd.Device, cmd.Method, cmd.Params)
    _ = gw.RespondCommand(ctx, cmd.CommandID, cmd.Device, result, 1)
})
```

### 6.1 Sub-device shadow (desired / reported)

A shadow is stored per **device UUID**, independent of transport: the
gateway's **own** shadow is direct-device traffic on `v1/me/shadow/*`; the
shadow of a sub-device it proxies uses the batched gateway topics below.

- Report one or more sub-devices' reported state on
  `v1/gateway/shadow/report`. A single sub-device is a length-one batch:

```json
{
  "reports": [
    {"device": "07704cf9-...", "state": {"led": "on", "brightness": 7}, "version": 3},
    {"device": "089fbb69-...", "state": {"rpm": 1200}}
  ]
}
```

  `state` is a per-top-level-key patch (a JSON `null` value deletes the key);
  `version` optionally acknowledges the desired version just applied. Entries
  are authorized and stored independently: an empty `device`, a non-UUID or
  not-attached device, or a non-object/empty `state` causes that entry alone
  to be dropped and audited — the rest of the batch still persists. A frame
  whose top-level JSON cannot be parsed is dropped wholesale.

- Subscribe once to `v1/gateway/shadow/desired`; demux each entry by
  `device`. Like the direct channel, desired is **always full-state**, not a
  delta — replace each named sub-device's local desired map wholesale:

```json
{"desired": [{"device": "07704cf9-...", "version": 3, "desired": {"led": "on"}}]}
```

  Desired is re-pushed in full when it changes and once when a sub-device
  becomes ONLINE with `desired_version > 0`, so no per-device get is needed.

Go SDK:

```go
_ = gw.SubscribeSubdeviceShadowDesired(ctx, 1, func(frame device.GatewayShadowDesiredFrame) {
    for _, d := range frame.Desired {
        applyLocalDesired(d.Device, d.Version, d.Desired) // full replace
    }
})

_ = gw.ReportSubdeviceShadow(ctx, []device.GatewayShadowReport{
    device.NewGatewayShadowReportForVersion("07704cf9-...", map[string]any{"led": "on"}, 3),
}, 1)
```

## 7. Connector lifecycle (desired / reported)

NereidLink is the system of record for connector instances; the gateway
applies desired state and reports what it actually runs.

- Subscribe the single filter `v1/gateway/connectors/#`. Desired specs
  arrive on `v1/gateway/connectors/spec`
  (`spec_id`, `connector_type`, `type_version`, `config`, `mappings`,
  `health_check`, `revision`), removals on `v1/gateway/connectors/delete`.
- After every hello the **full desired set and one bounded fair batch of
  unacknowledged explicit deletes are re-sent idempotently**; a leader-elected
  periodic sweep continues replaying delete batches. De-duplicate by
  `(spec_id, revision)`. NL commits desired removal and its durable delete
  tombstone before dispatch. There is no delete-ACK contract: transport
  success, REST `204`, timeout, ordinary `APPLIED`, and manifest absence never
  retire the tombstone.
- Report state on `v1/gateway/connectors/status` using the state machine
  `CREATING → APPLIED / FAILED / DEGRADED`, echoing `spec_id` and the current
  desired `revision`; stale or unknown-future revisions are ignored.
- Optional `stats` fields (`success_total`, `error_total`, `p50_ms`, `p95_ms`)
  are independent. Explicit zero counters are real readings, omitted fields
  are unavailable, and every accepted status replaces rather than merges the
  previous stats projection. `APPLIED` may additionally carry manager GET
  `observed_revision` and `observed_hash`; this hash is not the manifest
  config hash.

### Instance liveness probe (`health_check`)

A spec bound to one sub-device may carry `health_check`:

```json
{"resource": "camera_status", "interval_ms": 30000,
 "timeout_ms": 5000, "fail_threshold": 3}
```

- Every `interval_ms`, READ `resource` once for that sub-device and
  **discard the value**: never emit telemetry, shadow updates or any
  Envelope uplink from a probe.
- After `fail_threshold` consecutive failures/timeouts, assert the
  sub-device offline on `v1/gateway/disconnect`; the first successful
  probe afterwards asserts it back online on `v1/gateway/connect`
  (send state edges only).
- Stop probing when the field disappears or the spec is deleted; the
  probe is independent of the MQTT transport keepalive.

The console-side workflow is documented in the **Connectors** help topic.

### 7.1 Business orchestration expansion

[Business instances](help:glossary/business-instance) produce a deterministic [business expansion](help:glossary/business-expansion) that maps camera/pipeline/companion/rule assemblies into the same connector desired/reported lifecycle; they do not introduce a second connector transport. `inference_binding` artifacts therefore arrive as ordinary `edge-inference` specs and must report ordinary connector status.

Pipeline registration and reference-only external-servable registration use complete `edge-manager-resource` v1 connector envelopes with nested `spec.config`; external entries use `entry_id` plus `id_field=servableId`. A manager `APPLIED` report proves health only when it also carries the current `observed_revision` and a non-empty `observed_hash`; `DEGRADED/manager-entry-missing` remains degraded. Managed `ExternalServablesJSON` definitions fail closed until NI provides a runtime-only symmetric resource. Companion subscriptions remain separately gated by their signed delivery contract.

Northbound upload is declared independently from NI `PublishMode`/`PublishReadings`. The gateway egress must apply the signed channel and field-whitelist contract and must drop embeddings even if a misconfigured northbound declaration requests them.

A northbound node also supplies exact raw EdgeX selectors: `edgex_device_name`, `edgex_profile_name`, and a non-empty `edgex_resource_names` array. These are gateway-reported EdgeX names, not NereidLink UUIDs or canonical names. NL keeps each `northbound_channel` as a contribution, then reconciles one gateway-level `edge-northbound-filter` v1 connector with an empty `device_id`. Each contribution produces an explicit allow rule followed by a same device/profile deny rule; patterns, `PipelineJSON`, and case-sensitive `Embedding*` resources are rejected. Active and pending expansions across instances share the singleton. When the final contributor leaves, NL quiesces it with `enabled=false`, `default_action="allow"`, and `rules=[]` rather than hard-deleting it. Forward envelopes carry `channel` as metadata, while `forwarder_id` still selects the stream and sink.

## 8. Optional capabilities

| Capability | Downlink | Uplink | Notes |
|---|---|---|---|
| On-demand probe | `v1/gateway/probe` | `v1/gateway/probe/response` | Reachability test per sub-device; answer per device or one `responses[]` batch |
| Discovery / auto-provisioning | `v1/gateway/discovery/result` | `v1/gateway/discovery` | Report scan candidates; platform maps local IDs to UUIDs |
| OTA | `v1/gateway/ota/task` | `v1/gateway/ota/progress` | External firmware URL; ordered progress states |
| Profiles library | `v1/gateway/profiles/spec` | `v1/gateway/profiles`, `v1/gateway/profiles/status` | Report local profiles for adoption; receive shared library profiles (see §10.1) |
| Attribute push | `v1/gateway/attributes/push`, `v1/gateway/attributes/get/ack` | `v1/gateway/attributes`, `v1/gateway/attributes/get` | SERVER/SHARED delivered + CLIENT reported; `null` deletes; see section 5 |
| Shadow | `v1/gateway/shadow/desired` | `v1/gateway/shadow/report` | Desired/reported device shadow |
| Gateway operations | `v1/gateway/operations/invoke` | `v1/gateway/operations/result` | Synchronous whole-gateway actions; see section 9 |
| Edge model inference | (none — uses existing topics) | `v1/gateway/manifest` (`models`), `v1/gateway/connectors/status`, shadow reported `inference` | Report local model inventory; run the `edge-inference` connector; see section 8.1 |

Advertise only what you implement in the hello `features` list (for the
session-level features) and simply do not publish the rest.

### 8.1 Edge model inference (optional, ADR-0039)

A gateway that can serve local AI models negotiates the `inference` feature and
reports its inventory. The platform never transfers model bytes or initiates an
inference call; it stores the reported view, binds cameras to models through a
generic connector, and alarms on the model/runtime/binding planes. The full
contract is protocol v1 §15; the edge side is:

1. **Hello.** Add `"inference"` to `features.optional` and send a top-level
   `"inference": {"contract": "model/1", "backends": ["kserve", "openai"]}`
   block. Enable the feature only when the ack echoes an `inference` block
   (contract must equal `model/1`; `backends` is the intersection in your
   declared order). With no echoed block, never send a `models` segment and
   treat `edge-inference` specs as unsupported.
2. **Inventory.** In the existing `v1/gateway/manifest` frame add an optional
   full-replacement `models[]` segment alongside `devices`/`connectors`. Send
   the complete current set on the same cadence (debounced on change, full list
   every 5 minutes, replayed after (re)hello). Omit the field to keep the stored
   inventory untouched; send `[]` to authoritatively report zero models. Each
   entry is
   `{id, version, revision?, task, family?, backend, server_family?, model_name?, state, reason?, labels?, active_bindings?}`
   with `state` one of `EXTRACTING/VALIDATING/READY/FAILED/REMOVED`; a model that
   disappears from a present list is deleted server-side. Do not send this
   segment before hello or from a non-gateway identity. The optional
   `server_family` is the concrete model-server product token (e.g.
   `ovms`/`triton`/`external`), distinct from the `backend` protocol family;
   omit it when unknown — older platforms ignore the unknown key.
3. **Connector.** Self-register `connector_type="edge-inference"`,
   `type_version="1"` with its config JSON Schema in hello `connectors[]`. Desired
   specs arrive on the normal `v1/gateway/connectors/spec` topic with a mandatory
   `device_id` (a virtual output child device) and an opaque config carrying
   `source` (`{device, resource:"snapshot_jpeg", mode?, profile?}`, `mode` ∈
   `snapshot`|`stream`; stream maps to federated RTSP/ffmpeg, URL/credentials
   edge-local; `profile` is an optional ONVIF Media ProfileToken for stream
   mode, pattern `^[A-Za-z0-9_.:,-]+$`, ≤64, absent = camera default; enumerate
   tokens with the read-only `camera.profiles` v1 operation
   (`{source}` → `{profiles:[{token,name,encoding,width,height,fps}]}`),
   core-command `GetProfiles` with `Profiles` fallback, no URLs/addresses in
   the result; a third `mode:"url"` escape hatch binds non-ONVIF hardware
   without device/resource: `{mode:"url", transport, url, secret_name?}`
   with `transport` ∈ `rtsp`|`rtsp-udp`|`mpjpeg`|`jpeg`, `url` 8–1024 chars
   (`rtsp://` for RTSP transports, `http(s)://` for mpjpeg/jpeg, host
   required, embedded userinfo rejected → config-invalid), and optional
   `secret_name` (≤128, `^[A-Za-z0-9_.:,-]+$`) naming an operator-provisioned
   NI secret — the URL is platform-stored, credentials never are), `model_ref`,
   `trigger` (`event`|`periodic`|`continuous`)/
   `period_ms`/`max_idle_ms`/`fps` (0.1–60), thresholds, evidence-frame
   toggles `emit_frame`/`annotate_preview` (boolean default true, omitted in
   that case), ffmpeg tuning `ffmpeg_extra` for stream/direct-URL modes (≤256,
   allowlisted argv: -re or
   -r/-ss/-t/-threads/-fflags/-flags with a value, e.g. `-fflags nobuffer
   -flags low_delay`; invalid → config-invalid), site-local `decode_accel`
   (`software` default omitted / `vaapi`, ONVIF RTSP and direct rtsp/rtsp-udp
   only) plus optional `vaapi_device` (`renderD<N>` or `/dev/dri/renderD<N>`,
   omitted for NI's renderD128 default), mapped to NI `DecodeAccel` and
   `VAAPIDevice`; a missing VA-API device fails without software fallback, and
   the optional `target_device` (default `CPU`; `GPU`/`GPU.0`/`NPU`/`AUTO` or OVMS
   `MULTI:..`/`HETERO:..` forms, pattern `^[A-Za-z0-9_.:,-]+$`, ≤64 chars —
   declare the `mode`/`profile`/`fps`/`continuous`/`emit_frame`/`annotate_preview`/`ffmpeg_extra`/`decode_accel`/`vaapi_device`/`url`/`transport`/`secret_name`
   capabilities as well as this property
   in the self-registered config JSON Schema; absent `mode` means snapshot,
   absent `target_device` means `CPU`). A stream with no usable feed reports
   `source-no-frame` rather than silently switching acquisition;
   `target_device` is a hard placement requirement: report FAILED
   with `target-device-unavailable` instead of silently falling back to CPU.
   Report `APPLIED/DEGRADED/FAILED` plus a transport-safe `reason` on
   `v1/gateway/connectors/status`; there is no `health_check` — manage liveness
   from `max_idle_ms`/`period_ms`/`fps` (`idle-no-inference` is DEGRADED, not a fault),
   and report `model-not-ready` when a bound model is absent/not READY.
4. **Runtime health.** Keep the device-shadow reported `inference` block fresh:
   `{state: ONLINE|DEGRADED|OFFLINE|UNKNOWN, endpoint?, protocol, models_ready,
   models_total, active_bindings, checked_at (RFC3339), probe_interval_s,
   latency_ms?}`. Silence longer than three probe intervals is treated as
   UNKNOWN/stale server-side.
5. **Results & self-check.** Publish inference results as ordinary telemetry
   attributed to the bound virtual child device using the `detection/v1` system
   profile points (`detection_count`, `top1_label`, `top1_score`,
   `infer_latency_ms`, `detections_json`); evidence images use the existing Blob
   mechanism. Register the `inference.diag` v1 operation for on-demand fault
   isolation. Failure reasons use the shared code table in protocol v1 §15.6
   (`model-not-ready`, `runtime-unreachable`, `inference-timeout`,
   `source-no-frame`, `idle-no-inference`, `package-invalid`, …).

## 9. Gateway operations (whole-gateway actions)

Operations are immediate actions on the gateway itself (reboot, vendor
maintenance routines), distinct from sub-device commands (section 6). The
platform ships no operation vocabulary: advertise the operations your
build implements as catalog descriptors in hello `operations[]` — operation
key, interaction (`request-response`/`one-way`), timeout, parameter/result
JSON Schemas and risk (`read`/`normal`/`dangerous`).

The platform stores each gateway's descriptors **separately** (migration
0080); two gateways advertising the same key never overwrite each other, and
an invocation is validated against the TARGET gateway's own descriptor. Keep
these naming/version rules so a multi-vendor fleet stays consistent:

- `operations[]` is the gateway's **full current set** on every hello: keys
  absent from a later hello are reconciled away. Omit the field entirely
  (null) on firmware that does not speak operations; sending `[]` clears the
  set.
- `operation` + `type_version` is the contract: same key and version across
  gateways MUST have identical params, result, risk and interaction.
- Use standard namespaces for common actions — `diag.*` (diagnostics),
  `sys.*` (host info/maintenance), `service.*` (managed services); vendor
  actions MUST use `vendor.<vendor>.*` so they cannot collide.
- A breaking contract change needs a new `type_version` (or a new key, e.g.
  `diag.ping.v2`) since invoke frames do not select a version; when one
  gateway advertises several versions the newest one is invoked.
- The console aggregates the keys across gateways and flags a **contract
  conflict** when same-key advertisers disagree on risk/timeout/schema.

- Subscribe `v1/gateway/operations/invoke`. Invoke frames carry
  `id`, `operation`, `type_version`, `params` (already validated) and
  `timeout_ms`; the frame value is the effective bound — the smaller of the
  descriptor default and any operator per-call override, clamped to
  100–60000 ms. Reply on `v1/gateway/operations/result` with the same `id`,
  `ok`, and `result` or `error`.
- Execute only operations you advertised; answer every request-response
  invoke within `timeout_ms` — no reply means a platform-side `TIMEOUT`.
  For one-way operations send nothing.
- A parameter value of the form `secret://<name>` arrives already resolved
  to plaintext at the last mile; never log or persist it.
- Results MUST NOT contain passwords: the platform does not inspect result
  content, so redaction is the gateway's responsibility.
- Invocation is live-only: an offline gateway gets NOT_CONNECTED immediately
  and frames are never queued. Late/duplicate results are dropped.

```go
_ = gw.SubscribeOperations(ctx, 1, func(inv device.OperationInvoke) {
    out := runVendorOperation(inv.Operation, inv.Params)
    _ = gw.RespondOperation(ctx, device.OperationResult{
        ID: inv.ID, OK: true, Result: out,
    }, 1)
})
```

## 10. Topic summary

Gateway → platform: `v1/gateway/hello`, `heartbeat`, `goodbye`,
`linkhealth`, `connect`, `disconnect`, `manifest`, `telemetry`,
`attributes`, `shadow/report`, `command/response`, `ota/progress`,
`probe/response`, `connectors/status`, `secrets/status`, `profiles`,
`profiles/status`, `discovery`, `operations/result`.

Platform → gateway: `hello/ack`, `linkhealth/ack`, `command`,
`attributes/push`, `shadow/desired`, `ota/task`, `probe`,
`connectors/spec`, `connectors/delete`, `secrets/sync`,
`profiles/spec`, `discovery/result`, `operations/invoke`.

Uplink QoS is the gateway's choice (QoS 1 recommended); downlink frames are
QoS 0 and non-retained.

### 10.1 Device profiles delivered down (shared library)

A profile authored or reported anywhere in the tenant becomes a **library
asset**; the platform can hand it to any gateway that needs it, with no
re-authoring (e.g. gateway A reported `A1`, gateway B receives it). Opt in by
listing `profiles` in hello `features` and (optionally) your preferred
document formats in `profile_formats`; the ack returns the negotiated
`profile_formats`.

Subscribe `v1/gateway/profiles/spec`. Each frame is the **full desired set**
for this gateway and is re-sent after every hello:

```json
{"tenant_id": "…", "gateway_id": "…", "entries": [
  {"profile_id": "…", "name": "A1", "revision": 2,
   "format": "edgex-device-profile/3",
   "target": {"service": "device-modbus"},
   "document": {"name": "A1", "deviceResources": [ /* rendered */ ]}},
  {"profile_id": "…", "name": "Old", "revision": 1,
   "format": "edgex-device-profile/3", "removing": true}
]}
```

- `document` is **already rendered** in `format` (the first format both sides
  support). Supported today: `nereid-neutral/1` (always available; the
  `{name,protocol?,points[]}` shape you already report) and
  `edgex-device-profile/3` (EdgeX `deviceResources`/`deviceCommands`).
- Apply idempotently, de-duplicating by `(name, revision)`. `target` is an
  optional opaque hint (e.g. the device service to own the profile).
- `removing: true` carries **no** `document`: delete your local copy.
- Report back on `v1/gateway/profiles/status`:

```json
{"reports": [{"name": "A1", "revision": 2, "status": "APPLIED", "reason": ""}]}
```

  Status is one of `APPLIED`, `FAILED`, `REJECTED`, `REMOVED`; put a short,
  transport-safe explanation in `reason` for failures. The platform tracks
  `PENDING → DISPATCHED → APPLIED(/FAILED)` and raises alarms when a revision
  stays unapplied or keeps failing.
- The platform renders documents cloud-side; it never lands a profile into
  your device service — that is the agent's job.

## 11. Integration checklist

- Gateway registered with a credential; all sub-devices registered and
  bound to it.
- Hello sent after every (re)connect; no gateway frames published before
  `status: ok`; feature list matches what the build implements.
- LWT registered before CONNECT; planned goodbye on graceful shutdown;
  heartbeats at `limits.heartbeat_s`.
- Local link watchdog episodes reported after re-hello; ack subscription
  registered once; stop retrying on `accepted`/`duplicate`/`rejected`.
- `connect`/`disconnect` on attachment changes; full-state manifest every 5
  minutes with correct `connectors` presence semantics.
- Telemetry batches keyed by UUID, flat scalar values only.
- Commands: one subscription for all sub-devices; answer every
  request-response command by `command_id` + `device`, including failures;
  never answer one-way commands.
- Connector pushes de-duplicated by `(spec_id, revision)`; status reported
  through the state machine.
- Shadow: gateway's own shadow on `v1/me/shadow/*`; sub-device reports batched
  to `v1/gateway/shadow/report` and one `v1/gateway/shadow/desired`
  subscription demuxed by `device`; replace full desired, never patch it.
- Operations advertised in hello `operations[]`; every request-response
  invoke answered on `operations/result` by `id` within `timeout_ms`;
  results contain no passwords.
- If you opt into `profiles`: `v1/gateway/profiles/spec` subscribed, entries
  applied idempotently by `(name, revision)`, every applied (or removed) entry
  reported on `v1/gateway/profiles/status`.
