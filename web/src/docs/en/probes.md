<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Gateway Probes

A [**probe**](help:glossary/probe) is an on-demand reachability check: you ask a gateway to verify, right now, whether a set of its bound sub-devices answers on the field bus. The platform itself cannot see the field bus, so it groups the chosen devices by gateway, relays one aggregated request to each online gateway, and presents each gateway's per-device answers.

## Probe vs command vs liveness probe

These three are related but not interchangeable:

- **Probe** — answers only "is the device reachable, and what is the round-trip time". It carries no command name and requires no command catalog entry; the platform never interprets the result content. A failed probe changes no device state.
- **Command** — performs an actual read or write against a device (set a point, trigger an action). Commands must be declared in the command catalog and return business data; persistent commands are queued while the device is offline.
- **Liveness probe (`health_check`)** — a periodic, always-on probe configured on a connector instance that automatically drives the bound device's online/offline state. An on-demand probe is a single, manual batch check with no automatic state effect.

Probes for an offline gateway are not queued: those devices fail immediately with a not-connected error.

## Creating a probe

1. Open **Gateway Probes** and click **New probe**.
2. Pick the **gateway** that manages the devices. Gateways are listed with their live presence.
3. Select one or more **sub-devices** (up to 100 per task). Only ACTIVE sub-devices managed by the selected gateway are shown.
4. Choose a **mode**. The platform only relays this value; its exact behavior is defined by the gateway implementation:
   - **Cached** — the gateway may answer from a recent check.
   - **Fresh** (default) — the gateway performs a new check.
   - **Strict** — the gateway applies its strictest checking rules.
5. Set the **per-device timeout** in milliseconds (default 10000), then send.

## Filtering and searching

The toolbar above the table filters on the server:

- **State** — one aggregate state (pending, running, completed, partial, failed).
- **Mode** — cached, fresh or strict.
- A search box matching the gateway or probed device's name/alias (case-insensitive partial).

## Reading results

The task table shows each probe's creation time (formatted per the selected console language), the **gateway** it was created against (canonical name with alias beneath, linked to the gateway's detail page), mode, the **per-device timeout**, a live progress bar, succeeded/total counts and aggregate state:

- `Running` — still waiting for gateway answers.
- `Completed` — every device answered successfully.
- `Partial` — some devices failed or timed out.
- `Failed` — the task finished without success (for example every gateway involved was offline).

Click a row (or **View**) to open the result drawer: each row names the device (with its name and alias, linked to its detail page), per-device state, **Reachable** with round-trip time when the gateway reports it, and the failure error otherwise (timeout, no response, gateway not connected). Progress updates live over WebSocket while the task runs; task results are retained for 24 hours.
