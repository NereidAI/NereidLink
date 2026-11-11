-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0024 adds the TimescaleDB storage economics from ADR-0004: 7-day columnar
-- compression plus hourly and daily continuous aggregates.
--
-- Without this, ts_telemetry grows without bound in plain row format. ADR-0004
-- budgets roughly 440M rows/day at 3 measurements per frame, so an
-- uncompressed hypertable is not a scale story, it is an eventual
-- out-of-disk story. Compression and rollups are what make the single-PG
-- topology in ADR-0004 viable at all.
--
-- Design note -- why materialized_only = false:
--
-- A continuous aggregate normally only contains buckets that the refresh
-- worker has already materialized. A plain (materialized_only = true) view
-- would therefore silently return short result sets for any window that
-- reaches into the refresh lag, which is precisely the window operators ask
-- about most ("what did this device do an hour ago"). ADR-0004 calls for
-- "falling back to raw detail for intervals the rollup has not covered yet".
-- Real-time aggregates implement that rule inside the database: the view
-- serves materialized buckets and transparently unions live hypertable rows
-- past the watermark. Doing it in the database rather than in Go means the
-- query planner already knows the cheaper side of the union, and no
-- application code has to track a watermark and risk reading a stale
-- window.
--
-- Both aggregates are created WITH NO DATA on purpose: no history is
-- materialized here, because refresh_continuous_aggregate() cannot run inside
-- a transaction block and golang-migrate wraps every migration file in one.
-- `nereidlink migrate up` therefore performs the initial backfill as a
-- separate non-transactional step right after this file is applied. Until
-- that backfill completes, the telemetry read path keeps serving from the
-- hypertable -- see the coverage guard in internal/telemetry/series.go --
-- because an empty rollup would otherwise answer every historical window with
-- zero rows.
--
-- The daily rollup is hierarchical -- defined over the hourly rollup rather
-- than over the hypertable -- so the daily path reads ~24 rows per device-day
-- instead of re-scanning a day of samples, and the daily view stays correct
-- while the hourly view is refreshed beneath it.

-- ---------------------------------------------------------------------------
-- Hourly rollup
-- ---------------------------------------------------------------------------
--
-- The aggregate set deliberately mirrors the Series() query in
-- internal/telemetry/series.go: last() of each value carrier, grouped by
-- tenant/device/measurement. Matching it exactly is what makes query
-- routing a pure relation swap. If Series ever needs a different aggregate
-- (min/max/avg), both the rollup and the raw query must change together or
-- the chart will silently change shape depending on the requested window --
-- a failure mode worth avoiding by keeping this SELECT a mirror of that one.

--
-- GROUP BY is positional here on purpose. Writing "GROUP BY bucket" looks
-- equivalent but is not: the daily view selects from ts_telemetry_hourly,
-- which already HAS a column named bucket, so the name resolves to the source
-- column instead of the new time_bucket() expression. The bucketing
-- expression is then absent from the GROUP BY and TimescaleDB rejects the
-- view with "must include a valid time bucket function" -- an error that
-- points nowhere near the real cause. Positional grouping cannot be
-- misresolved. The hourly view gets away with the alias form only because
-- ts_telemetry has no column called bucket, which is exactly what makes this
-- bug easy to reintroduce by copy-paste.

CREATE MATERIALIZED VIEW IF NOT EXISTS ts_telemetry_hourly
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket(INTERVAL '1 hour', time) AS bucket,
       tenant_id,
       device_id,
       measurement_key,
       last(v_double, time) AS v_double,
       last(v_bool, time) AS v_bool,
       last(v_string, time) AS v_string
FROM ts_telemetry
GROUP BY 1, 2, 3, 4
WITH NO DATA;

-- ---------------------------------------------------------------------------
-- Daily rollup, hierarchical over the hourly rollup
-- ---------------------------------------------------------------------------

CREATE MATERIALIZED VIEW IF NOT EXISTS ts_telemetry_daily
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT time_bucket(INTERVAL '1 day', bucket) AS bucket,
       tenant_id,
       device_id,
       measurement_key,
       last(v_double, bucket) AS v_double,
       last(v_bool, bucket) AS v_bool,
       last(v_string, bucket) AS v_string
FROM ts_telemetry_hourly
GROUP BY 1, 2, 3, 4
WITH NO DATA;

-- ---------------------------------------------------------------------------
-- Refresh policies
-- ---------------------------------------------------------------------------
--
-- TimescaleDB rejects a policy whose refresh window is not strictly larger
-- than one bucket ("policy refresh window too small"), so start_offset and
-- end_offset are spaced two buckets apart rather than one. That also matches
-- ADR-0004's requirement to refresh the most recent two buckets: late-arriving
-- ingest and clock skew can push a row into an already-materialized bucket,
-- and without a second bucket of re-refresh that row would stay invisible in
-- the materialized region forever.
--
-- end_offset holds one full bucket back so the bucket ingest is still
-- appending to is never rewritten. Readers are unaffected: the real-time
-- union in these views serves anything past the watermark.

-- Hourly rollup: refresh every 10 minutes over a 2-bucket window.
SELECT add_continuous_aggregate_policy('ts_telemetry_hourly',
                                      start_offset => INTERVAL '3 hours',
                                      end_offset => INTERVAL '1 hour',
                                      schedule_interval => INTERVAL '10 minutes',
                                      if_not_exists => TRUE);

-- Daily rollup: refresh hourly over a 2-bucket window.
SELECT add_continuous_aggregate_policy('ts_telemetry_daily',
                                      start_offset => INTERVAL '3 days',
                                      end_offset => INTERVAL '1 day',
                                      schedule_interval => INTERVAL '1 hour',
                                      if_not_exists => TRUE);

-- ---------------------------------------------------------------------------
-- Columnar compression
-- ---------------------------------------------------------------------------
--
-- segmentby is (device_id, measurement_key) per ADR-0004: those two columns
-- are the query predicate for essentially every read, so keeping them
-- uncompressed turns the filter into a metadata lookup instead of a scan.
-- time DESC is the orderby because history reads are "newest first within a
-- device" and compressed chunks can then satisfy the sort from the index
-- instead of re-sorting.
--
-- tenant_id is deliberately NOT a segmentby column. Adding it would split
-- every segment, and the tenant predicate is satisfied by the device_id
-- lookup that always accompanies it.

ALTER TABLE ts_telemetry SET (
    timescaledb.compress,
    timescaledb.compress_segmentby = 'device_id, measurement_key',
    timescaledb.compress_orderby = 'time DESC'
);

-- Compress chunks once they are 7 days old, per ADR-0004.
SELECT add_compression_policy('ts_telemetry',
                              INTERVAL '7 days',
                              if_not_exists => TRUE);
