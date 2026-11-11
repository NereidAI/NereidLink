<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Overview

The Overview is the tenant operational dashboard. It answers "what is happening right now?" at a glance.

## KPI cards

- **Devices** — total registered devices, with online/offline split. Click the card to open the device list.
- **Live alarms** — alarms in `ACTIVE` or `ACK` state. Click to open the alarm center filtered to live alarms.
- **Pending commands** — commands not yet in a terminal state (`PENDING`, `CLAIMED`, `QUEUED`).
- **Unread notifications** — in-app notifications you have not marked as read.
- **Telemetry 1h** — points received in the last hour; the helper line shows the 24-hour total.

## Panels

- **Telemetry throughput** — point count in 5-minute buckets.
- **Live alarms by severity** — current alarm distribution.
- **Recent live alarms / presence / notifications** — the newest events.

## How updates work

The first load is a single REST snapshot. After that, values update through one wildcard WebSocket subscription per resource — there is no polling. Panels can be dragged by their title bar and resized from the bottom-right corner; the layout is stored locally in your browser.

## Console navigation

The left navigation scrolls independently when its entries exceed the available height. The version footer remains at the bottom of the sidebar, including when the navigation is collapsed.
