-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back 0024.
--
-- Data safety notes, in order of importance:
--
-- 1. Dropping a continuous aggregate DISCARDS its materialized contents. It is
--    not recomputed from the hypertable. That is safe here only because the
--    aggregates are pure derived data: every bucket can be rebuilt from
--    ts_telemetry by re-creating the view and refreshing it. No source data
--    is lost, and the next `nereidlink migrate up` rebuilds both rollups.
--
-- 2. Compressed chunks are NOT decompressed by this rollback. Undoing the
--    compression settings does not rewrite already-compressed chunks, and
--    decompressing them would be a long-running rewrite of the largest table
--    in the system. Compressed data remains fully queryable either way, so
--    leaving it compressed is correct rather than merely convenient. An
--    operator who needs the raw row format back must run
--    decompress_chunk() explicitly and take the write lock that implies.

-- Remove the refresh policies before the aggregates: a policy referencing a
-- dropped view would otherwise block the drop.
SELECT remove_continuous_aggregate_policy('ts_telemetry_hourly',
                                         if_exists => TRUE);
SELECT remove_continuous_aggregate_policy('ts_telemetry_daily',
                                         if_exists => TRUE);

DROP MATERIALIZED VIEW IF EXISTS ts_telemetry_daily;
DROP MATERIALIZED VIEW IF EXISTS ts_telemetry_hourly;

-- Drop the compression policy so the scheduler stops trying to compress new
-- chunks, then clear the table-level settings.
SELECT remove_compression_policy('ts_telemetry', if_exists => TRUE);

ALTER TABLE ts_telemetry SET (
    timescaledb.compress = false
);
