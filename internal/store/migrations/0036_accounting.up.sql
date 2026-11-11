-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0036_accounting.up.sql adds the ADR-0025 resource-accounting store: a
-- cluster-wide Top-N hypertable fed by the per-node 1/N samplers.
--
-- Each row is one dimension value reported by one node for one window.
-- The rows are deliberately the *aggregated* heavy-hitter candidates (not
-- raw traffic): every node already reduced its own stream to a bounded
-- Top-K with a Space-Saving sketch before publishing, so the write volume
-- is bounded by nodes x dimensions x K per window and never by traffic.
--
-- Retention is fixed at 24h per ADR-0025 (accounting is a live
-- troubleshooting aid, not an archive). A TimescaleDB retention policy
-- drops whole chunks; the explicit sweep below only backstops the case
-- where the background worker has not run yet in a short-lived
-- deployment.

CREATE TABLE accounting_topn (
    time TIMESTAMPTZ NOT NULL,
    node_id TEXT NOT NULL,
    dimension TEXT NOT NULL,
    dimension_value TEXT NOT NULL,
    message_count BIGINT NOT NULL,
    byte_count BIGINT NOT NULL DEFAULT 0,
    sample_rate BIGINT NOT NULL DEFAULT 1,
    window_seconds INTEGER NOT NULL DEFAULT 60
);

-- The diagnostic query is always "one dimension, one time window, top N",
-- so the leading columns match that access path exactly.
SELECT create_hypertable('accounting_topn', 'time', if_not_exists => TRUE);

CREATE INDEX accounting_topn_dimension_time_idx
    ON accounting_topn (dimension, time DESC);

-- Enforce the supported dimension set in the database as well, so a
-- future caller cannot silently invent a new unbounded series.
ALTER TABLE accounting_topn
    ADD CONSTRAINT accounting_topn_dimension_chk
    CHECK (dimension IN ('tenant','device','ip','protocol','node','topic'));

ALTER TABLE accounting_topn
    ADD CONSTRAINT accounting_topn_window_chk
    CHECK (window_seconds > 0);

-- 24h retention. drop_chunks keeps only whole chunks, so the effective
-- live span is 24h rounded up to the chunk interval.
SELECT add_retention_policy('accounting_topn', INTERVAL '24 hours', if_not_exists => TRUE);