<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Link Health

The **Link health** tab on a [gateway](help:glossary/gateway) detail page lists the history of the gateway's link outages as time intervals. It answers "when did the link drop, how long did each outage last, and who detected it" — questions the live presence badge cannot answer.

## What each row means

- **Down at** — the start of the outage.
- **Recovered at** — the end; an **Ongoing** tag means the outage is still open and its duration is still growing.
- **Duration** — recovered minus down, rendered compactly (`45s`, `12m 30s`, `2h 5m`).
- **Source** — who detected the outage:
  - **Lease detector** — the platform detected it from an expired gateway lease (heartbeats stopped or the transport dropped).
  - **Edge watchdog** — the gateway's local watchdog reported the episode over `v1/gateway/linkhealth`. When an edge report matches a lease episode, the row is corrected with the edge ground truth and shown as edge-sourced; a micro-flap the lease never saw appears as an edge-only row.
- **Reason** — the recorded reason text.
- **Alarm** — long outages link to the gateway's **Alarms** tab; alarm-free sub-lease micro-flaps show none. An edge flap burst is represented by a `GATEWAY_LINK_FLAPPING` alarm; see the Alarms help.

## Filtering

- Use the **Source** dropdown to show only lease-detected or edge-reported episodes.
- Use the **time-range picker** to narrow to a specific window.
- Pages load server-side; an open outage stays on the first page until it ends and the list is refetched.

## Relation to other tabs

- The presence badge and the Devices page show only the **current** online/offline state.
- The **Alarms** tab tracks alarm lifecycle (open/ack/clear); an outage can exist without an alarm, and one burst can drive one alarm across multiple episodes.
- Alarm-free micro-flaps are visible only here and, aggregated, in the tenant's daily outage digest.
