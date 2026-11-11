<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Quarantine

Quarantine is a reversible incident-response lock on a single device. Use it when a device is suspected to be compromised, stolen, flooding, or behaving anomalously — and you need to stop it now without destroying its history.

## What quarantine does

- Sets the device to the `QUARANTINED` [lifecycle state](help:glossary/lifecycle).
- Evicts [presence](help:glossary/presence) and disconnects the live session immediately.
- Makes further authentication fail: the device cannot reconnect, send [telemetry](help:glossary/telemetry), or receive commands, even though its credentials are still on record.
- Raises a CRITICAL alarm.
- Records who quarantined it, when, and why.

Quarantine is **not** decommissioning. Identity, ownership, telemetry and history are all preserved, and the device can be released back to service without re-provisioning.

## Reason codes

A quarantine requires a structured reason code plus a free-text note:

| Code | Use when |
| --- | --- |
| `credential-compromise` | A credential is suspected leaked or misused. |
| `device-stolen` | The device is believed to be in hostile hands. |
| `burst-flood` | The device exceeds its declared rate or burst budget. |
| `firmware-anomaly` | Suspicious device behaviour. |
| `other` | Catch-all. **A note is mandatory** here, so the code alone never carries the whole story. |

## Releasing a device

There are two paths, both of which clear the CRITICAL alarm and record an audit event:

- **Direct release** — a platform administrator releases the device from the Devices page, providing a resolution note explaining why it is safe to restore. Use this when the incident is understood and closed.
- **Dual-control release** — the release is opened as an operation order from the CLI by a platform operator and confirmed by the super administrator in the web console. Use this when the decision needs separation of duties. See **Operations Orders** under System administration.

Either way, the device returns to its previous active state and its existing credentials work again — no re-issuance is required.

## Seeing quarantined devices

Quarantined devices appear in the default device list with a `QUARANTINED` badge, the quarantine timestamp and the reason. They are deliberately not hidden: an incident nobody can see is not being handled.

## Notes

- A quarantined device is excluded from telemetry writes and command dispatch, not merely disconnected.
- Releasing does not restore anything that was deleted; quarantine deletes nothing.
