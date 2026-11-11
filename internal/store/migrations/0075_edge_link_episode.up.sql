-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0075_edge_link_episode.up.sql lets edge-local link watchdog episodes
-- (reported on v1/gateway/linkhealth) correct or supplement the lease-based
-- gateway_link_episode rows from migration 0039.
--
-- Two facts change:
--   * alarm_id becomes nullable. Lease episodes always raise a deduplicated
--     alarm, but an edge-reported outage that the server lease never saw
--     (a sub-90s flap) is recorded as a finished, non-alarmed episode so it
--     still counts in the 24h digest without paging anyone.
--   * source / edge_down_at record where the row came from. 'lease' rows are
--     the original server-guessed episodes; when an edge report matches one,
--     its boundaries are overwritten with the edge ground truth and the row
--     is marked 'edge' with the reported down time. An edge report with no
--     matching lease episode is inserted directly as a closed 'edge' row.
--
-- The partial unique index de-duplicates retransmissions and backlog replay
-- per (gateway, edge_down_at): the same outage is recorded at most once.

ALTER TABLE gateway_link_episode
    ALTER COLUMN alarm_id DROP NOT NULL;

ALTER TABLE gateway_link_episode
    ADD COLUMN source TEXT NOT NULL DEFAULT 'lease',
    ADD COLUMN edge_down_at TIMESTAMPTZ;

ALTER TABLE gateway_link_episode
    ADD CONSTRAINT gateway_link_episode_source_check
    CHECK (source IN ('lease', 'edge'));

CREATE UNIQUE INDEX gateway_link_episode_edge_down_idx
    ON gateway_link_episode (gateway_id, edge_down_at)
    WHERE edge_down_at IS NOT NULL;
