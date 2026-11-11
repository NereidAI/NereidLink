<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device Services

The **Device Services** page is the visible, editable catalog of the protocol families and reference [EdgeX](https://docs.edgexfoundry.org/4.0.2/microservices/device/DeviceService/) device services NereidLink knows how to render structured forms for.

## What the catalog is

Each catalog entry (a **protocol family**) records:

- **Family key** — the open-vocabulary protocol family stored on a [device profile](help:glossary/profile), e.g. `s7`, `modbus`.
- **Display name** and **service name** — the reference EdgeX device service (e.g. `device-s7`) used to seed a blank [connector](help:glossary/connector) template's `ds_name`.
- **Protocol key / transports** — the concrete keys that may appear under an EdgeX device's `protocols` map (e.g. `s7`, `modbus-tcp`).
- **Connection parameters** — structured fields rendered in the profile/device connector editor and emitted verbatim into the protocol property map.
- **Point address parameters** — structured fields rendered per point descriptor and emitted into the point address object.
- **Default point address** — the initial address object prefilled when a new point is created (e.g. `{"NodeName": "DB4.DBX0.0"}`). Leave it empty to derive defaults from each parameter's own default value.
- A link to the official documentation.

The catalog is **global**: the same vendor facts apply to every tenant. The platform kernel itself parses no protocol; the catalog only drives UI forms and suggestions. Ingestion and parsing stay transport-independent.

## Built-in vs custom

- The fourteen device services listed by the EdgeX 4.0.2 device-service index are seeded as **Official** entries: Modbus, ONVIF camera, MQTT, REST, device-virtual, SNMP, BACnet, CoAP, GPIO, OPC-UA, RFID LLRP, S7, UART and USB camera.
- Official entries **can be edited** (to correct a label, add a field) but **cannot be deleted**.
- Use **Add device service** to define a custom family; custom entries can be edited and deleted.

## How it relates to device profiles

Selecting a profile's **protocol family** unlocks that family's structured connection parameters (on the connector template and gateway connector instance) and point-address fields (on each descriptor). Families with no fixed properties, or families not in the catalog, fall back to free JSON editing; unknown content is never rejected.

## S7 notes

- `device-s7` speaks ISO-on-TCP. Connection properties are `Host`, `Port` (102), `Rack`, `Slot` and `Timeout`/`IdleTimeout` as **integer seconds** (not duration strings).
- A point address uses a single attribute `NodeName`, e.g. `DB4.DBX0.0`, `DB4.DBD4`.
- A pasted EdgeX device profile whose resources carry `NodeName` is auto-tagged family `s7`. A gateway-reported minimal frame that omits attributes cannot be auto-tagged until the edge includes them.
