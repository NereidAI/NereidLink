-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0011 adds a dedicated boolean value column so devices can report
-- boolean telemetry (switch states, presence flags) without coercing it
-- into a 0/1 double. The column is nullable; existing rows are left
-- untouched and remain queryable through v_double / v_string.

ALTER TABLE ts_telemetry ADD COLUMN IF NOT EXISTS v_bool BOOLEAN;

-- Keys discovery selects the newest row per measurement key. This partial
-- index keeps that DISTINCT ON lookup on the device/time ordering cheap.
CREATE INDEX IF NOT EXISTS ts_telemetry_device_key_time_idx
    ON ts_telemetry (device_id, measurement_key, time DESC);
