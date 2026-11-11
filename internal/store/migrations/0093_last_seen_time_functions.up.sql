-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0093_last_seen_time_functions.up.sql adds plpgsql helpers that return the
-- most recent observation time for one device on each time-series hypertable.
--
-- Why a function instead of an inline correlated subquery: inside the device
-- list/detail statements the correlation column is planned as an internal
-- parameter, i.e. a generic plan that never sees the concrete device id.
-- TimescaleDB's ChunkAppend then trusts the per-column statistics (~2M rows
-- per device inside a 6M-row chunk) and chooses the time-only index plus a
-- device_id filter, instead of the existing (device_id, time DESC) index.
-- Devices with no rows in the newest chunk each pay a multi-million-row
-- filter (~4.4s per device, ~40s per list, including empty pages).
--
-- Dynamic EXECUTE inside plpgsql plans the statement afresh on every call
-- with the concrete id bound as a parameter, which makes the planner pick
-- the (device_id, time DESC) index; the full device-list shape was measured
-- at ~7ms versus ~40s (see L-150). The functions are STABLE because the
-- result cannot change within one statement. SECURITY INVOKER is the default
-- and is intentional: callers keep their own table privileges.

CREATE OR REPLACE FUNCTION last_telemetry_time(p_device_id uuid)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_last timestamptz;
BEGIN
    EXECUTE 'SELECT max(time) FROM ts_telemetry WHERE device_id = $1'
        INTO v_last
        USING p_device_id;
    RETURN v_last;
END;
$$;

CREATE OR REPLACE FUNCTION last_presence_time(p_device_id uuid)
RETURNS timestamptz
LANGUAGE plpgsql
STABLE
AS $$
DECLARE
    v_last timestamptz;
BEGIN
    EXECUTE 'SELECT max(time) FROM ts_presence WHERE device_id = $1'
        INTO v_last
        USING p_device_id;
    RETURN v_last;
END;
$$;
