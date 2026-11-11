<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Direct Device Integration

This guide explains how to connect a **directly connected device** (firmware
that speaks to NereidLink itself, with no gateway in between). It is written
for device/edge developers, not console operators.

- **Transport:** MQTT 3.1.1 / 5.0 (primary) or HTTPS long-polling (fallback).
- **Payloads:** JSON, UTF-8.
- **Official SDK:** Go — `github.com/nereidai/nereidlink/sdk/device/go`. It is
  a thin framing/transport library; your firmware keeps control of cadence
  and reconnection. Other languages can implement the wire contract in this
  document directly.

## 1. Prerequisites on the platform

Before firmware can connect, an operator must prepare the device in the
console:

1. Create the device and assign a **[device profile](help:glossary/profile)** (its
   measurement [descriptors](help:glossary/profile) declare point names, value
   types and units).
2. Create at least one **credential** for the device. Three kinds exist:
   - `TOKEN` — one opaque access token, sent as the MQTT username with an
     empty password (or HTTP `Authorization` header).
   - ID + secret — sent as MQTT username/password or HTTP Basic auth.
   - X.509 client certificate — mutual TLS.
3. A device without a valid credential cannot establish a session.

Telemetry keys should match the measurement keys declared in the device
profile. The platform never guesses units or types: only descriptor-defined
points get conversions/units; unknown scalar keys are still stored but
without transformation.

## 2. MQTT quick start (Go SDK)

```bash
go get github.com/nereidai/nereidlink/sdk/device/go@latest
```

```go
package main

import (
    "context"

    "github.com/nereidai/nereidlink/sdk/device/go"
    "github.com/nereidai/nereidlink/sdk/device/go/devicemqtt"
)

func main() {
    ctx := context.Background()

    client, err := devicemqtt.Connect(ctx, devicemqtt.Options{
        BrokerURL:  "tcp://nereidlink.example:1883", // or tls://...:8883
        Credential: device.Token("device-access-token"),
        ClientID:   "sensor-001",
    })
    if err != nil {
        panic(err)
    }
    defer client.Disconnect(200)

    // Receive downlink commands and answer them.
    _ = client.SubscribeCommands(ctx, 1, func(cmd device.Command) {
        // Downlink topic: v1/me/cmd/<name>; reply path uses the ID.
        _ = client.RespondCommand(ctx, cmd.ID,
            map[string]any{"ok": true}, 1)
    })

    // Publish flat scalar measurements.
    frame := device.NewFrame().
        SetFloat("temperature", 21.5).
        SetBool("online", true).
        SetString("mode", "AUTO")
    _ = client.PublishTelemetry(ctx, frame, 1)
}
```

`device.Basic(id, secret)` and `device.ClientCert(...)` provide the other
credential kinds. A complete runnable program is in the SDK under
`examples/simple`.

## 3. MQTT topic contract

A directly connected device only ever uses the `v1/me/*` topics.

### Device → platform

| Topic | Direction | Payload |
|---|---|---|
| `v1/me/telemetry` | publish | Flat JSON object of scalar measurements |
| `v1/me/events` | publish | One domain-event object or an array |
| `v1/me/attributes` | publish | CLIENT-scope attributes object |
| `v1/me/attributes/get` | publish | On-demand pull of platform attributes: `{"scopes":[...],"response_topic"?}`; omitted `scopes` means SERVER_SCOPE+SHARED_SCOPE |
| `v1/me/shadow/report` | publish | Reported-state patch: `{"state":{...},"version"?:N}` |
| `v1/me/cmdres/{command_id}` | publish | Result of a request-response command |

### Platform → device (subscribe)

| Filter/topic | Payload |
|---|---|
| `v1/me/cmd/#` | Command frames; the concrete topic is `v1/me/cmd/<command_name>` |
| `v1/me/attributes/down` | SERVER/SHARED attribute pushes |
| `v1/me/attributes/get/ack` | On-demand pull reply: `{"scopes":{"SERVER_SCOPE":{...},"SHARED_SCOPE":{...}}}` |
| `v1/me/shadow/desired` | Complete desired state: `{"version":N,"desired":{...}}` |

Downlink frames are QoS 0 and non-retained. The firmware chooses uplink QoS;
QoS 1 is recommended.

### Telemetry frame

```json
{
  "ts": 1728374400000,
  "temperature": 21.5,
  "online": true,
  "mode": "AUTO"
}
```

- Every value MUST be a JSON number, boolean or string. `null`, nested
  objects and arrays are rejected before upload.
- `ts` is optional (epoch milliseconds). Omit it to let the platform stamp
  arrival time.
- One frame produces one stored record per measurement key.

### Attributes and scopes

Attributes are relatively static key/value configuration or metadata, not
telemetry: the device only reports the **CLIENT** scope via
`v1/me/attributes`; the platform manages **SERVER_SCOPE** and
**SHARED_SCOPE**.

In the current version both SERVER_SCOPE and SHARED_SCOPE **are delivered to
the device**: while online you receive incremental pushes on
`v1/me/attributes/down` as `{"scope":"...","data":{"key":value}}`; after every
(re)connect you should publish `v1/me/attributes/get` once to recover changes
missed while offline, and replace the local cache wholesale with the full
key/value set returned per scope in `get/ack`. Note this differs from classic
ThingsBoard semantics, where SERVER_SCOPE is private to the server and only
SHARED_SCOPE is visible to the device; do not assume SERVER_SCOPE is private
here. A JSON `null` value in a downlink `data` map means the platform deleted
that key, so the device must drop the local cache entry (do not store null).
Omitting `scopes` in the request pulls both platform scopes; requesting CLIENT
or an unknown scope is rejected.

### Device shadow (desired / reported)

The shadow holds, per device UUID, a platform-requested **desired** state and
the device-observed **reported** state, so you can tell "the state I asked
for" from "the state it actually reached". Use it only for state that must be
maintained and confirmed; use commands for one-shot actions and SHARED
attributes for fire-and-forget configuration.

- **Desired is always full-state.** On `v1/me/shadow/desired` you receive the
  complete map as `{"version":N,"desired":{...}}`; replace your local desired
  map wholesale, never patch it. The frame carries no tenant/device fields —
  the connection already identifies the device. Desired is re-pushed in full
  whenever it changes and once on each ONLINE (re)connect with
  `desired_version > 0`.
- **Reported is a patch.** Publish `v1/me/shadow/report` with
  `{"state":{...},"version"?:N}`; `state` is merged per top-level key. Add
  `version` to acknowledge the desired version you just applied. A JSON
  `null` value deletes that top-level key; values may be scalars, objects or
  arrays.
- The platform computes `delta` (desired keys whose reported value differs)
  and flags the shadow `in_sync`; read both over REST/WebSocket.

### Media: images, video, text and other binaries

Binary values never go into the time-series row (no bytea column, no
inline payload). The bytes are stored in the platform's blob backend and
the point keeps a string reference `blob://<key>`; read-side clients
resolve the reference to a short-lived signed URL. Two uplink forms are
provided.

**Form 1 — frame marker (base64 inside a telemetry frame).** Replace a
scalar value with this object:

```json
{
  "photo": {
    "blob": true,
    "content_type": "image/jpeg",
    "filename": "camera-01.jpg",
    "data": "/9j/4AAQ..."
  },
  "temperature": 23.4
}
```

Marker fields:

| Field | Required | Meaning |
|---|---|---|
| `blob` | yes | Must be the boolean `true`; any other value rejects the field. |
| `data` | yes | The file bytes **encoded as base64**. Plain text here is NOT accepted: `"HELLO WORLD."` must be sent as `"SEVMTE8gV09STGQu"`. Empty data is rejected. |
| `content_type` | no | The media type of the bytes. Omit it (or send `""`) to let the platform detect the type from the content; the platform never guesses units but does store/detect a media type. |
| `filename` | no | Original file name, display only. It never influences the stored object key. |

Accepted base64 variants: standard or URL-safe alphabet, with or without
padding (`SEVMTE8gV09STGQu`, `SEVMTE8gV09STGQ`, `SEVMTE8gV09STGQu`).
The marker works over MQTT (`v1/me/telemetry`) and HTTP
(`POST /v1/me/telemetry`); it can be mixed with ordinary scalar values in
the same frame.

**Form 2 — direct binary upload (no base64).**

```bash
curl -X POST "http://<host>/v1/me/blob?key=photo" \
  -H "Authorization: Bearer <device-token>" \
  -H "Content-Type: image/jpeg" \
  --data-binary @camera-01.jpg
```

The request body is the raw file, streamed straight into blob storage.
The `key` query parameter (default `blob`) is the measurement name and
must match `[A-Za-z0-9_.-]{1,128}`; the stored object key is always
generated server-side. The response:

```json
{"published":1,"key":"media/<tenant>/<device>/<uuid>",
 "uri":"blob://media/<tenant>/<device>/<uuid>","size":48213}
```

**Size and type policy** (platform-configured, enforced once on the
server):

| Setting | Default | Meaning |
|---|---|---|
| `blob.media_max_bytes` | `10485760` (10 MiB) | Max size of one media object. |
| `blob.media_allowed_types` | `image/` | Allowed content types; an entry ending in `/` is a type prefix (image/, video/), others match exactly. |

Over-sized objects return `413`; disallowed types return `415` (frame
marker) or are rejected; when blob storage is disabled a marked field
returns an error — bytes are never inlined.

**Reading media back.** The reference is the point's string value
`blob://media/<tenant>/<device>/<uuid>`.

- The device itself: `GET /v1/me/blob?key=<key>`. Only objects owned by
  the authenticated device are served; HTTP Range, ETag and conditional
  requests are supported.
- Console/UI users: `GET /v1/tenants/{tenant}/blob/sign?key=<key>`
  (`device:read` permission) → `{"url","size","content_type"}`. The URL is
  a presigned S3 URL or an HMAC-signed `/v1/blobs/get` URL, valid ~15
  minutes. The console renders images as thumbnails, video in a player,
  text/JSON/XML in a text viewer (first 256 KiB inline), and other types
  as downloads.

### Command frame (platform → device)

```json
{
  "id": "01J8...",
  "name": "set-led",
  "direction": "write",
  "params": { "color": "green" }
}
```

- The **topic suffix is the command `name`** (e.g. `v1/me/cmd/set-led`);
  the **reply topic suffix is the command `id`**
  (`v1/me/cmdres/01J8...`). Never swap them.
- `direction` is `read` or `write`, the south operation to perform, and is
  orthogonal to whether a reply is expected. Execute the GET/write exactly
  as directed; do not infer it from the name. A read may still carry
  `params` (query arguments), and `one-way` frames never carry
  `direction=read`.
- `params` is a JSON document validated against the platform catalog; it is
  delivered verbatim.
- For a **request-response** command, publish an arbitrary JSON result to
  `v1/me/cmdres/{id}`. For a **one-way** command, do not reply.
- If the command cannot be executed, still reply (request-response mode)
  with a JSON body describing the failure; the platform marks the command
  `SUCCEEDED` once any correlated response arrives, so include an explicit
  error field for business failures, e.g. `{"error": "busy"}`.

### Domain events

```json
{
  "event_type": "door_forced",
  "severity": "warning",
  "ts_ms": 1728374400000,
  "payload": { "zone": 3 }
}
```

`severity` is one of `info`, `warning`, `error`, `critical` (default
`info`). The platform carries no business logic: `event_type` and `payload`
are tenant-defined opaque data.

## 4. HTTP transport

For networks that only allow HTTPS, the device channel provides:

- `POST /v1/me/telemetry` — upload one flat telemetry frame (base64 media
  markers allowed; see the Media section).
- `POST /v1/me/blob?key=<measurement>` — upload raw media bytes, and
  `GET /v1/me/blob?key=<key>` — download the device's own object (Range
  supported).
- `GET /v1/me/commands` — one long-poll wait; returns either the next queued
  command or an empty envelope when the wait window expires.
- `POST /v1/me/cmdres/{command_id}` — submit a command result.
- `POST /v1/me/shadow/report` — report shadow state.
- `GET /v1/me/ota/tasks`, `GET /v1/me/ota/{task_id}/package`
  (Range resume supported), `POST /v1/me/ota/{task_id}/progress` — firmware
  update flow.

```go
httpClient, _ := devicehttp.New(devicehttp.Options{
    BaseURL:    "https://nereidlink.example",
    Credential: device.Token("device-access-token"),
})

_, _ = httpClient.PublishTelemetry(ctx,
    device.NewFrame().SetFloat("temperature", 21.5))

for {
    cmd, _ := httpClient.PollCommands(ctx) // one long poll; loop to re-arm
    if cmd != nil {
        _ = httpClient.RespondCommand(ctx, cmd.ID,
            map[string]any{"ok": true})
    }
}
```

Authentication uses the same credentials (the token goes in
`Authorization`).

## 5. Command timeliness

Commands are declared in the platform's **command catalog** per device;
undeclared command names cannot be dispatched (the platform rejects them
with 404). Each declaration selects:

- **Interaction:** `one-way` (fire-and-forget, ends `DELIVERED`) or
  `request-response` (waits for one correlated result, then `SUCCEEDED` or
  `TIMEOUT`).
- **Timeliness:** `realtime` (an offline device immediately gets
  `NOT_CONNECTED`) or `persistent` (queued and delivered after reconnect;
  undelivered commands past TTL end `EXPIRED`).

Firmware does not configure these; it only observes whether a reply is
expected.

## 6. Integration checklist

- Device created in console with a profile; credential issued and embedded
  in firmware.
- Firmware connects with the correct credential and unique client ID.
- Telemetry frames are flat scalar JSON, keys match profile descriptors,
  `ts` included when sampling time matters.
- Binary media uses the blob path: base64 frame marker or
  `POST /v1/me/blob`; never send raw text in marker `data`, and verify the
  content type is allowed (`blob.media_allowed_types`).
- Firmware subscribes to `v1/me/cmd/#`, distinguishes `name` (topic) from
  `id` (reply), and always answers request-response commands — including
  failure cases.
- Reconnection is handled by firmware (the SDK offers auto-reconnect for
  MQTT); after reconnect, resume publishing with fresh timestamps.
