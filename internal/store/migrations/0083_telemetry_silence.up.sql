-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0083 adds ADR-0014 online-but-silent detection.
--
-- A measurement descriptor may declare how often the point is expected to
-- report. NULL (the default) leaves the point unmonitored. A positive value,
-- in seconds, enables silence monitoring for devices using this profile.
-- tolerance_multiplier widens the deadline to absorb normal jitter; the
-- effective deadline is expected_period_seconds * tolerance_multiplier.
ALTER TABLE measurement_descriptor
    ADD COLUMN IF NOT EXISTS expected_period_seconds INTEGER,
    ADD COLUMN IF NOT EXISTS tolerance_multiplier DOUBLE PRECISION NOT NULL DEFAULT 3;

ALTER TABLE measurement_descriptor
    DROP CONSTRAINT IF EXISTS measurement_descriptor_expected_period_check;
ALTER TABLE measurement_descriptor
    ADD CONSTRAINT measurement_descriptor_expected_period_check
        CHECK (expected_period_seconds IS NULL OR expected_period_seconds > 0);

ALTER TABLE measurement_descriptor
    DROP CONSTRAINT IF EXISTS measurement_descriptor_tolerance_check;
ALTER TABLE measurement_descriptor
    ADD CONSTRAINT measurement_descriptor_tolerance_check
        CHECK (tolerance_multiplier >= 1);

-- Partial index used by the silence sweep join: only monitored descriptors
-- participate, and the shortest period on a profile decides the deadline.
CREATE INDEX IF NOT EXISTS measurement_descriptor_period_idx
    ON measurement_descriptor (profile_id, expected_period_seconds)
    WHERE expected_period_seconds IS NOT NULL;

-- Last accepted telemetry time per device. The writer maintains it in the
-- same transaction as the hypertable insert, so a periodic sweep can detect
-- online-but-silent devices from an index instead of scanning ts_telemetry.
-- NULL until the first point is stored.
ALTER TABLE device
    ADD COLUMN IF NOT EXISTS last_telemetry_at TIMESTAMPTZ;
