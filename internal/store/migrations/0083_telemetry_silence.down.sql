-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back 0083: remove the silence-detection descriptor settings and the
-- per-device last-telemetry watermark.

DROP INDEX IF EXISTS measurement_descriptor_period_idx;

ALTER TABLE measurement_descriptor
    DROP CONSTRAINT IF EXISTS measurement_descriptor_tolerance_check;
ALTER TABLE measurement_descriptor
    DROP CONSTRAINT IF EXISTS measurement_descriptor_expected_period_check;

ALTER TABLE measurement_descriptor
    DROP COLUMN IF EXISTS tolerance_multiplier,
    DROP COLUMN IF EXISTS expected_period_seconds;

ALTER TABLE device
    DROP COLUMN IF EXISTS last_telemetry_at;
