<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Statistics Center

The Statistics Center provides detailed numbers beyond the Overview KPIs. It has two tabs.

## System tab

Point-in-time infrastructure metrics:

- **Go runtime** — uptime, goroutines, heap, GC cycles and pause.
- **PostgreSQL** — server connections, application pool state, transaction totals, buffer hit ratio, deadlocks.
- **NATS** — messages and bytes in both directions, client reconnects.
- **JetStream streams** — stored messages, sequence ranges and per-consumer backlog (pending, redelivered, ack floor).
- **MQTT broker** — type and number of connected clients.

## Business tab

Tenant-wide activity for a chosen time range (last 1 hour, 24 hours or 7 days):

- Fleet totals and devices by profile.
- Telemetry volume timeline.
- Alarms by state and severity; commands by current state.
- Count of active rule chains.

## Refresh

Both tabs refresh automatically every 30 seconds. Use the pause button to stop auto-refresh, or the reload button to refresh immediately.

## For monitoring systems

The same metrics are exposed in Prometheus format at `/metrics`. Point Prometheus at that endpoint and import the Grafana dashboard from `deploy/monitoring/`.
