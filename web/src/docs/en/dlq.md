<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Dead Letter Queue

The Dead Letter Queue (DLQ) collects [telemetry](help:glossary/telemetry) points rejected by a failing storage hook whose failure policy is `dlq`.

## Columns

- **Time** — when the point was received.
- **Device** — originating device ID.
- **Key** — measurement key.
- **Hook** — the hook that rejected the point.
- **Reason** — failure detail.

## Actions

Select one or more rows, then:

- **Replay** — re-inserts the points into telemetry, bypassing hooks, so they are processed and stored.
- **Delete** — removes the rows permanently.

Replaying is useful after fixing a hook or its downstream service. Regularly review the queue so device data is not silently lost.
