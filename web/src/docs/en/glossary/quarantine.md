<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Quarantine

**Quarantine** is a reversible incident-response lock on a single device. Use it when a device is suspected compromised, stolen, flooding, or behaving anomalously and must be stopped now without losing history.

Quarantine sets the device to the `QUARANTINED` [lifecycle state](help:glossary/lifecycle), evicts its [presence](help:glossary/presence) and live session, and makes further authentication fail — the device cannot reconnect, send [telemetry](help:glossary/telemetry), or receive commands while its credentials stay on record. Identity, ownership, telemetry and history are preserved, and releasing the device returns it to service without re-provisioning.

Quarantine is a lifecycle action, distinct from the [connection state](help:glossary/presence). See the [Quarantine](help:page/quarantine) guide for reason codes and release procedure.
