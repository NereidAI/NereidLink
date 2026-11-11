<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Alarms

The Alarms page is the alarm lifecycle and audit center.

## Filtering

- **Live** — alarms currently `ACTIVE` or `ACK`.
- Individual states: `ACTIVE`, `ACK` or `CLEARED`.
- Optionally filter by device, severity (`CRITICAL`, `MAJOR`, `MINOR`, `WARNING`), alarm type and assignee (including **Unassigned**).
- All active filters are reflected in the URL so the filtered view can be bookmarked or shared.

## Acknowledging

Select **Ack** on a live alarm to mark it acknowledged. Acknowledging records the acknowledgment time; acknowledged alarms remain live until cleared. Acknowledging an already-ACK alarm is a no-op.

## How alarms are created

Alarms are opened by the **Create Alarm** node in a [rule chain](help:glossary/rule-chain). Repeated events with the same dedupe key update the open alarm instead of creating duplicates; clearing requires the **Clear Alarm** node.
