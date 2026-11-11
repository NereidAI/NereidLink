<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Connector

A **connector** is a protocol-connector *instance* running on a [gateway](help:glossary/gateway) and managed through the platform. It is the concrete realization of a connector type on one gateway.

- A connector is either **gateway-level** (not bound to a device) or bound to exactly one managed [sub-device](help:glossary/subdevice).
- The platform is the system of record: you declare a **desired spec** `{connector_type, type_version, config, mappings}`, and the gateway reports the state it actually applied (`CREATED`, `DISPATCHED`, `APPLIED`, `DEGRADED`, `FAILED`).
- A connector's `config` and `mappings` are interpreted only by the edge; the platform never understands protocol details.

The bound [device profile](help:glossary/profile) supplies the reusable point table; instance overrides are merged over it at dispatch. [Health checks](help:glossary/health-check) (`health_check`) are available only on connectors bound to a device.

See the [Connectors](help:page/connectors) page guide for full operations.
