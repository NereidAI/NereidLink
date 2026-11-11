<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Connectors

The Connectors tab is available on **[gateway](help:glossary/gateway) devices**. It manages protocol [connector](help:glossary/connector) instances running on that gateway using a declarative desired/reported model (ADR-0026).

## Concepts

- NereidLink is the system of record. You declare a **desired spec**, and the edge gateway reports back what it has actually applied.
- A desired spec is `{connector_type, type_version, config, mappings}`.
  - **connector_type** — the connector plugin type, for example `modbus-tcp`. New connector types are registered by the edge; the kernel never interprets protocol details.
  - **type_version** — plugin version; leave empty to use the highest registered version.
  - **config** — a JSON object validated against the edge-registered schema for the connector type.
  - **mappings** — a JSON array of objects; every object must contain a non-empty string `key` naming the [telemetry](help:glossary/telemetry) point the connector maps to. The editor suggests that `key` (and each EdgeX auto event's source name) from the bound profile's [descriptor](help:glossary/profile) measurement keys; you can still type any value. Additional properties are connector-specific (register address, source name, and so on).
- **Two-level configuration**: a [profile](help:glossary/profile) stores the reusable **template / point table**, and a connector instance stores per-device connection parameters. At dispatch the instance overrides are merged over the profile template. A connector can therefore be created without a profile by filling `config` and `mappings` directly.
- A connector is either **gateway-level** (no bound device) or bound to one of the gateway's managed [sub-devices](help:glossary/subdevice).

### Built-in type: `nereid-edge`

`nereid-edge` is always present in the type picker, even before any gateway registers a schema. It identifies the built-in NereidEdge runtime (the EdgeX App Service), whose behavior is hard-wired, so its config and mappings are usually left as the defaults — `{}` and `[]`. Other types (for example `modbus-tcp`, `onvif`, `mqtt-remote`) only appear once a connected gateway registers them, but you may still type any custom value.

For EdgeX-family connector types (`nereid-edge` and the `edge-connector`/`edgex-*` aliases) the structured editor renders the standard EdgeX device fields (the v4 device contract NereidEdge applies to core-metadata): device service name (`ds_name`), EdgeX profile name, device name, description, labels, protocols and auto events (advanced fields such as `tags` and `properties` remain editable in the JSON tab). On a gateway, the **EdgeX profile name** is a searchable pick-and-type box populated from the profiles that gateway has reported (`v1/gateway/profiles`); pick one and each auto event's **source name** is then offered from that profile's device resources. You can always type a value that was not reported. **Protocols** are one card per EdgeX protocol key. When the bound profile (or the profile-template selection) declares a known protocol family, the card offers structured connection fields and a transport selector (for Modbus: `modbus-tcp`/`modbus-rtu`/`modbus-ascii` with `Address`/`Port`/`UnitID`, serial `BaudRate`/`DataBits`/`StopBits`/`Parity` on RTU/ASCII, and timeout fields); the same dictionary also renders ONVIF (`Onvif`: `Address`/`Port`/`MACAddress`/`FriendlyName`/`EndpointRefAddress`), MQTT (`mqtt`: `CommandTopic`), REST (`REST`: `Host`/`Port`/`Path`/`urlRawQuery`), device-virtual (`other`: `Address`/`Port`), SNMP (`TCP`: `Address`/`Port`), BACnet (`BACnet-IP`/`BACnet-MSTP`: `DeviceInstance`/`Port`/`MAC`) and CoAP (`COAP`: `ED_ADDR`/`ED_SecurityMode`/`ED_PskKey`); extra keys stay editable as typed pairs. For an unknown family you pick `modbus-tcp`/`opcua`/`mqtt`/… or type a custom key, and every property value is typed as string, number or boolean. **Auto events** cover the full EdgeX `AutoEvent` shape: interval (e.g. `10s`), source name, an `onChange` switch that reveals its threshold, and an advanced retention section (`maxCap`, `minCap`, duration). When an instance targets one bound sub-device and the EdgeX **device name** is left blank, the console fills it on save with that sub-device's NL name (`canonical_name`). On a bulk create over several sub-devices (multi-select or Select all in device scope), a blank device name is filled **per device**: every created connector gets its own sub-device's NL name, so each edge device is named correctly. The name is sanitized to EdgeX-legal characters (alphanumerics, `_`, `-`); characters such as the `/` in an auto-provisioned name like `<gateway>/<local_id>` become `-`. Typing an explicit device name always overrides this default.

## Writing config and mappings

Each JSON field opens in a **split editor**: structured inputs on the left and a read-only live JSON document on the right. A **JSON** mode remains the escape hatch for keys no structured field covers; switching back requires the text to parse and satisfy the entry contract (object vs array, and the edge-registered JSON Schema when available).

For a connector instance bound to a **profile template**, the structured side edits only this instance's overrides, while the right pane previews the **effective document** — the profile template deep-merged with the override, exactly as it will be dispatched (objects merge key by key; arrays and scalars replace; a non-empty instance mappings array replaces the template list).

`config` is a JSON **object**; `mappings` is a JSON **array** whose items are objects that each carry a non-empty string `key`. The exact other keys depend on the connector type and are validated by the edge against the schema it registered — the platform does not interpret them.

Built-in `nereid-edge` (config/mappings can stay default):

```json
{ }
```

```json
[ ]
```

A typical Modbus TCP connector:

```json
{ "host": "192.0.2.10", "port": 502, "unit_id": 1, "byte_order": "BIG" }
```

```json
[
  { "register": "40001", "key": "voltage" },
  { "register": "40002", "key": "current" }
]
```

With two-level configuration, put the common part (bus settings, point table) in the **profile template** and only the per-device differences (this PLC's `host`/`port`) in the instance config; objects are deep-merged and a non-empty instance mappings array replaces the template array.

## Lifecycle state

`PENDING → DISPATCHED → CREATING → APPLIED`, with `FAILED` or `DEGRADED` as terminal/degraded outcomes. Desired specs are never dropped: the edge retries on reconnect, and dispatch is idempotent. Explicit deletes are committed as durable revisioned tombstones before dispatch and replayed in bounded fair batches after hello and from the periodic sweep; transport success, timeout, an ordinary apply acknowledgement, and manifest absence do not acknowledge deletion. Hover a state tag to see the reported reason.

Status reports are accepted only for the current desired revision. Optional job statistics preserve availability independently for success/error counters and P50/P95 latency: zero is a real reading, while an omitted field is unavailable. Manager resources can additionally report the observed manager revision and complete-readback hash; these are separate from manifest reconciliation hashes.

## Reconciliation

The gateway periodically sends a manifest listing the connector instances it actually runs. The tab compares that **reported** set against the desired set and classifies each spec:

- **In sync** — desired and reported match.
- **Missing** — desired but not present on the gateway.
- **Stale** — the reported revision is behind the desired revision.
- **Drifted** — present, but its reported config differs from desired.
- **Extra** — running on the gateway but not declared in NereidLink.

By default drift only raises an alert; in **strict reconcile** mode the desired spec overrides missing, stale, or drifted desired instances. **Extra** entries remain observational even in strict mode: a type-less manifest identity cannot safely authorize deletion of legacy, manager, or northbound-filter resources. Until the gateway sends its first manifest there is no watermark, so classification shows as unknown and an informational banner is displayed. The tab has no refresh button: it stays live over the WebSocket `connector` stream, re-fetching the list and reconcile view whenever the gateway reports a desired/status/manifest change.

The **Device profile / protocol** column names the device profile behind the connector (the bound profile template, falling back to the bound sub-device's profile) with a link to its detail page, and its protocol family underneath when known; it shows `-` when no profile resolves.

The **Bound sub-device** column links to the bound device with its alias underneath; gateway-level instances show a gateway-level tag instead.

## Filtering and searching

The toolbar above the table is right-aligned and filters locally over the gateway's connector set:

- **Connector type**, **reported state**, **reconcile state** and **target** (gateway-level vs bound sub-device) dropdowns.
- A search box matching connector type, type version, profile name/protocol and bound sub-device name (case-insensitive, partial).

Filtering never changes the data; clear a dropdown or the search box to return to the full list.

## Working with connectors

1. Open a gateway device and select the **Connectors** tab.
2. Click **Add**, choose (or type) the connector type, and optionally a type version.
3. Optionally pick a **profile template** — only profiles carrying a connector template are listed — and/or a bound sub-device.
4. Edit the config object and point mappings using the structured Form tab (or raw JSON tab); the save button stays disabled until both are valid, then save.
5. Edit or delete a row as needed. A successful delete response means desired removal and durable delete intent were committed, not that the edge acknowledged removal; NL keeps replaying the explicit delete under the current protocol.

To define a reusable connector template, edit a device profile and fill its connector template fields; see the **Device Profiles** help topic.

## Liveness probe ([health check](help:glossary/health-check))

A connector bound to one sub-device can run an application-layer liveness probe. Enable **Liveness probe** in the connector drawer and fill:

- **Probe resource** — the south read resource/command name to ping.
- **Interval** — probe cadence in milliseconds (default 30000).
- **Read timeout** — per-probe timeout in milliseconds; must stay below the interval (default 5000).
- **Fail threshold** — consecutive failures that mark the bound device offline (default 3); the next successful probe marks it online again.

The gateway periodically reads the resource and **discards the value** — a probe never produces telemetry or shadow data. Connect/disconnect is asserted by the gateway itself, exactly like an operator observing the field bus. The policy lives on the connector instance (not on the profile template) because probing is a per-device operational choice; disable the switch to stop probing.
