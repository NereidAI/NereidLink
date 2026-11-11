<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device Profiles

A [device profile](help:glossary/profile) groups measurement descriptors: how raw device values map to engineering values.

## Concepts

- **Descriptor** — one measurement key with unit, precision and a value transform.
- Transform types:
  - **identity** — stored as published.
  - **linear** — `scale * raw + offset`.
  - **table** — piecewise linear breakpoints between raw keys.
  - **expression** — a JavaScript expression, for example `point.v_double * 9 / 5 + 32`.
  - **enum** — raw value mapped to a text label.
- Descriptors are versioned; recalibration never changes already-stored history.
- A profile may bind a **[rule chain](help:glossary/rule-chain)**; devices using the profile run it unless they set their own binding.
- A profile may optionally carry a **[connector](help:glossary/connector) template** (ADR-0026 two-level configuration) so it can be reused by [gateway](help:glossary/gateway) connectors.
- A **protocol family** (open vocabulary, e.g. `modbus`, `onvif`) unlocks structured connection and point-address fields. It is editable on **every** source, including edge/import profiles: a discovery frame commonly omits the family, so the operator fills it in by hand. The vendor-owned point specification (data type, access, range) remains source-owned on edge/import.

## Protocol family and point specification

The protocol family is vendor-neutral assistance, not platform logic: NereidLink still stores everything opaquely. A known family (the built-in dictionary covers the current official EdgeX device services — **Modbus, ONVIF, MQTT, REST, device-virtual, SNMP, BACnet, CoAP**) replaces free-form JSON with structured inputs.

- On each measurement descriptor, the **Point specification** section carries the vendor-neutral fields `data type` (`bool`/`int`/`uint`/`float`/`string`/`binary`), access (`R`/`W`/`RW`), reported default and engineering min/max, plus the protocol-specific **address**.
- For Modbus the address fields are `primaryTable` (`COILS`, `DISCRETE_INPUTS`, `DISCRETES_INPUT`, `HOLDING_REGISTERS`, `INPUT_REGISTERS`), `startingAddress`, `isByteSwap`, `isWordSwap`, optional `rawType` and `stringRegisterSize`. Any non-dictionary address keys you previously had remain editable as extra JSON.
- Families whose points use free-form attributes (ONVIF `{service, function}`, MQTT, REST, virtual, SNMP, BACnet, CoAP) and any unknown family keep the address as one raw JSON object (e.g. an OPC-UA `{ "nodeId": "..." }`).
- The data type / access / default / range fields are editable only on **manual** profiles and stay source-owned on edge/import profiles. The **protocol family and every point's address are editable on all sources** (manual, edge and import), because a discovery/import frame frequently omits both; the calibration fields (transform/scale/offset/enum/valid range) always remain editable. An accepted edge report adopts only the values the frame actually fills, so a minimal replay never wipes an address or family you entered.

## Profile detail page

A profile is opened by clicking its name in the Profiles list, which navigates to its own page (`/profiles/<id>`). The page presents three read-first sections, each edited independently with its own **Edit**/**Save** — there is no long tabbed dialog:

- **Basic information** — name, protocol family and rule chain.
- **Connection parameters** — the connector template below.
- **Point descriptors (n)** — the point list.

Creating a profile only asks for a name (and optionally the protocol family); after creation you land on the detail page to fill in the connector and points. Each section saves the whole profile server-side, so saving one section never changes the others.

## Connector template (optional)

The **Connection parameters** section of the detail page carries an optional reusable connector template:

- **Connector type** — the connector plugin type. The built-in `nereid-edge` (the hard-wired NereidEdge runtime) is always listed; other types are autocompleted from those registered by connected gateways, and you may type any custom value.
- **Type version** — the plugin version. Leave empty to select the highest registered version at dispatch time.
- **Config (JSON)** — a JSON **object** holding the shared template configuration. With `nereid-edge` selected it opens in a split view: a structured EdgeX device form on the left and a live JSON document on the right (a raw-JSON tab remains as an escape hatch).
- **Point mappings (JSON)** — a JSON **array** of objects, each carrying a non-empty string `key` plus connector-specific properties.

When the EdgeX form first opens on an empty config it is pre-filled once with convenience defaults, and any existing value is never overwritten:

- **Device service name** (`ds_name`) — the family's official EdgeX device-service name (Modbus → `device-modbus`, ONVIF → `device-onvif-camera`); filled once the protocol family is known.
- **Profile name** — derived from the profile name with EdgeX-legal characters (alphanumerics, `_`, `-`). It is a starting point for the template. The **device name is not template-seeded**: it is per-device and the connector-instance editor injects it from the bound sub-device's NL name (`canonical_name`, sanitized) when left blank. Clearing a pre-filled field keeps it cleared.
- Auto-event **source name** and point-mapping **key** offer searchable suggestions built from this profile's descriptor measurement keys (equivalently EdgeX device resource names), while staying free text.

Example config and mappings (see the **Connectors** help topic for the built-in `nereid-edge` defaults and a full Modbus example):

```json
{ "port": 502, "byte_order": "BIG" }
```

```json
[ { "register": "40001", "key": "voltage" } ]
```

Leave the connector type empty to keep a **telemetry-only profile**. The kernel never interprets the config or mappings; the edge validates them against the schema registered for the connector type. Only profiles that carry a connector type are listed in the **Profile template** selector of a gateway's Connectors tab. See the **Connectors** help topic for the desired/reported lifecycle and reconciliation.

## Creating a profile

1. Click **Create profile** and give it a name.
2. Add descriptors with the key exactly as the device publishes it.
3. Configure the transform and, for table/enum types, the breakpoints or entries.
4. Optionally bind a rule chain; leave it empty to use the tenant default chain.
5. Optionally fill in a connector template (type, version, config, mappings) to make the profile reusable by gateway connectors.
6. Assign the profile to devices from the device editor.

Devices without a profile keep working; their values are stored untransformed.

## Online-but-silent detection

Each point descriptor has an optional **Expected period** (seconds) and a
**Silence tolerance** multiplier (default 3). Set the period for points that
must report on a schedule, such as a meter reading every 60 seconds.

- A device using the profile is considered silent when it is connected but no
  telemetry arrives within `expected period × tolerance` of the most recent
  reading. The platform then raises an `ONLINE_BUT_SILENT` warning alarm.
- Leave the period empty to exclude a point from this check. The shortest
  expected period among the profile's monitored points sets the device's
  deadline.
- The alarm clears automatically when telemetry resumes or the device goes
  offline; it is distinct from an OFFLINE presence state.
