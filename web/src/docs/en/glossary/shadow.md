<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device shadow

The **device shadow** stores two independent documents for a device:

- **desired** — the state the platform wants the device to reach, written by operators and [rule chains](help:glossary/rule-chain).
- **reported** — the state the device last reported itself; written only by the device.

Because the two are independent, issuing a command never blocks waiting for the device to acknowledge. Per-key status shows **synced** when desired equals reported and **pending** when the device has not caught up.
