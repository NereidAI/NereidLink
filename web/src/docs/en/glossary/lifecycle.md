<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Lifecycle state

A device's **lifecycle state** is its operational status, as opposed to its live connection state.

- `ACTIVE` — in service; the default device-list view shows only active devices.
- `QUARANTINED` — reversibly locked by [quarantine](help:glossary/quarantine).
- Decommissioned/archived states are **tombstones**: devices are never physically deleted, so history and identity remain auditable.

Lifecycle is independent of [presence](help:glossary/presence): an active device may be offline, and connectivity says nothing about whether the device is in service.
