<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Probe

A **probe** is an on-demand, one-shot reachability check of a [sub-device](help:glossary/subdevice): the platform asks its [gateway](help:glossary/gateway) to perform a real read on the field bus and report whether the device answered and the round-trip time. The platform only relays the request; it never interprets or stores the reading as business data.

## Probe vs presence

[Presence](help:glossary/presence) is the platform's continuous, cached connection state. For a passive sub-device it reflects only what its gateway asserts while the gateway's own platform session is alive — a device that drops off the field bus (power loss, wiring fault) can still appear online until the gateway notices via a [health check](help:glossary/health-check). A probe instead proves end-to-end application reachability at the moment it runs and returns the measured round-trip time. It never updates presence or any other device state.

## Probe vs health check

- **Probe** — manual, one-off batch check; results are shown only to the operator.
- **Health check** — a periodic probe loop configured on a connector instance that automatically drives the bound device's online/offline state.

## Probe modes

The platform only validates and forwards `mode`; its precise semantics are defined by the edge implementation.

| Mode | Recommended meaning |
|---|---|
| `cached` | Answer from the most recent known check; no new field-bus read is required. |
| `fresh` (default) | Perform a new read on the field bus now. |
| `strict` | Perform a new read under the strictest checking rules. |

A probe targets only sub-devices bound to a gateway. Devices on an offline gateway are not queued; they fail immediately with a not-connected error.
