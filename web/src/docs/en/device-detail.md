<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device Detail

The device detail page shows everything known about one device.

## Header

- Canonical name, device ID, profile ID and current [connection state](help:glossary/presence). When the device has an alias, the header subtitle shows the alias instead of the ID.
- The summary panel shows every binding of the device:
  - **Customer** — the owning [customer](help:page/customers); empty means unassigned. The name links to the Customers page.
  - **Device group** — the one device group this device belongs to (groups form a tree and may be anchored to a customer); links to the device list filtered to that group.
  - **Rule chain** — the chain group that actually applies to this device, resolved by priority: device binding, then profile binding, then the tenant default group. A tag states the binding source; the version tag shows the active version, or a **No active version** warning when the resolved group has no active chain. The name opens the chain editor.
  - **Profile ID** — links to the bound profile.
  Use the **Edit** button in the header to change any of these bindings in the device form drawer.
- The summary panel also shows **additional information**, plus **created at** and **last activity** (the latest [telemetry](help:glossary/telemetry) or presence boundary, whichever is newer). The alias is shown only in the header subtitle, not repeated in the panel.
- A [sub-device](help:glossary/subdevice) summary includes a **[Connector](help:glossary/connector)** line: its one bound connector instance with reported apply state, linking to the owning [gateway](help:glossary/gateway)'s **Connectors** tab; a gateway shows the count badge of its gateway-level connector instances.
- The status badge reflects the latest presence event (online/offline/unknown).

## Tabs

### Telemetry

Real-time telemetry pushed over the WebSocket. Select a measurement key to follow one measurement. Historical values load through the telemetry history query. The **All measurements** table shows each value's quality marker (`GOOD`, `UNCERTAIN` or `BAD`); see the **Data Quality** help topic under Configuration for what they mean.

A string value shaped `blob://...` references a media object the device uploaded (see direct-device integration). The table renders it by the object's declared type: an image thumbnail (click to enlarge), a play button that opens a video player, a text viewer for text/JSON/XML (first 256 KiB inline), or a download button for other binaries. Each object is reached through a short-lived signed URL; if the object was deleted, the cell shows an "object no longer exists" note.

### Attributes

Attributes are relatively static per-device key/value metadata, suited to install location, business grouping, sampling interval, feature flags and similar small configuration. They differ from high-frequency telemetry and from the versioned desired/reported shadow: the platform only stores and synchronizes the values, while NereidEdge/firmware decides how an attribute is consumed.

The three scopes are:

- **CLIENT**: device-owned data written by the device or rule chains. It is shown read-only in the console and cannot be added, edited or deleted here.
- **SHARED_SCOPE**: written by the platform and pushed over MQTT to an online device/gateway; devices also pull it after reconnecting. Configuration that the edge must read and apply belongs in this scope.
- **SERVER_SCOPE**: platform-side attributes written by the platform.

> **Current scope semantics:** this NereidLink version currently delivers **both SERVER_SCOPE and SHARED_SCOPE to devices** — via live `attributes/down|push` and the post-reconnect `attributes/get`. This differs from the classic ThingsBoard model, where SERVER_SCOPE is platform-private and only SHARED_SCOPE is visible to devices. Do not treat SERVER_SCOPE as device-invisible private data today; use SHARED_SCOPE for configuration the edge should consume. For gateway-attached sub-devices, platform attributes are also mirrored into EdgeX device Tags; when the same key exists in both scopes, the SHARED_SCOPE value wins.

Add or edit SERVER_SCOPE/SHARED_SCOPE attributes with any valid JSON value. The actions column offers edit and delete; deleting removes the row immediately and pushes a JSON `null` change to a connected edge, which evicts the local cache key. The table updates live over the WebSocket: edits/deletes from another operator session and device-reported CLIENT values appear immediately without refreshing.

### Shadow

Desired versus reported state for this device, with a write path for desired state. The view updates live over the WebSocket: desired changes from another operator session and device-reported state appear immediately without refreshing. See the **Device Shadow** help topic under Devices.

### Credentials

Every credential this device holds, its type, validity window and status, plus create, rotate, disable and revoke actions. See the **Device Credentials** help topic under Devices.

### Commands

The device command catalog (whitelist) and command history. Only declared commands can be sent — an undeclared name is rejected. Each catalog entry records its interaction (one-way/request-response), timeliness, default timeout, enabled switch and the allowed direction(s) (read/write); entries tagged **auto** are materialized from the bound profile's points and become **manual** once edited. When sending, pick the command (bound profile points are shown with their read/write mask), choose read or write (the choice is limited by the catalog mask), and always review the JSON parameters box (`{}` by default; reads may carry query parameters). Profile points without a catalog entry can be declared inline from the send drawer. History shows direction and state; open a row to inspect parameters, response and error.

### Alarms

Alarm audit rows for this device with type, severity, state and timestamps.

### Connector & secrets (sub-device only)

A read-only view for a gateway-managed sub-device of the two platform surfaces its connection depends on. **Bound connector** shows the single connector instance bound to this sub-device (matched from the owning gateway's connector set): type and version, reported apply state (hover for the reason), desired/reported revision and last report time. **Secret delivery** lists every secret binding the owning gateway holds — secret, type, target selectors and status — with a note that selectors are open `{kind, value}` pairs interpreted by the edge, so a binding may or may not apply to this device. Both sections link to the owning gateway's **Connectors** and **Secrets** tabs, where delivery is actually managed. This tab is shown only for sub-devices; see the **Connectors** and **Edge secrets** help topics under Devices.

### Firmware OTA (direct devices only)

Shown only for a directly connected device (not a gateway and not a gateway-managed sub-device). It lists every firmware rollout that targeted this device, newest first: target version, the device's current phase, progress bar, the parent rollout's aggregate state and creation time. Open **Timeline** to see the append-only phase history with the structured result code and who reported each phase. **Upgrade this device** starts a single-device rollout — the target is locked to this device; pick a platform-hosted package or supply an external URL, and a success policy. Direct devices discover the job by polling an authenticated endpoint and download the bytes through the authorized relay. Fleet-wide rollouts and task-level pause/cancel live on the **Upgrade Tasks** page; see the **OTA tasks** help topic under Operations.

### Managed devices (gateway only)

Sub-devices whose presence is declared by this gateway. This tab is shown only for gateway devices. Each row links to the sub-device and shows its alias beneath the name, its profile (links to the profile), the bound connector (links to this gateway's Connectors tab), the connection status (hover for last activity), and the lifecycle state. The search box matches name, alias or device id.

### Connectors (gateway only)

Protocol connector instances running on this gateway, managed as desired/reported specs with drift reconciliation. This tab is shown only for gateway devices; see the **Connectors** help topic under Devices.

### Link health (gateway only)

The history of this gateway's link outages rendered as time intervals — when each drop started, how long it lasted and whether the lease detector or the edge watchdog detected it. This tab is shown only for gateway devices; see the [Link health](help:page/link-health) help topic.

### Secrets (gateway only)

The edge secrets (vault references) delivered to this gateway and their per-binding delivery status, including the secret types the edge registers. This tab is shown only for gateway devices; see the [Edge secrets](help:page/edge-secrets) and [Edge secret types](help:page/edge-secret-types) help topics.

### Edge profiles (gateway only)

The device profiles this gateway has reported over MQTT, cached per gateway, with content/inspect and adopt-or-sync-into-library actions. This tab is shown only for gateway devices; see the [Edge profiles](help:page/edge-profiles) help topic.

### Operations (gateway only)

The remote operations this gateway's firmware advertised in its last hello (plus tenant-defined descriptors), invoked from here with schema-driven parameters, risk gating and per-invocation history. This tab is shown only for gateway devices; see the [Gateway operations](help:page/gateway-operations) help topic.

## Quarantine

Quarantined devices show a `QUARANTINED` badge with the quarantine time and reason, and offer a release action. See the [Quarantine](help:page/quarantine) help topic under Devices.
