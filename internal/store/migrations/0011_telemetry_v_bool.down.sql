-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0011: drop the boolean column and the keys-discovery index.
-- Stored boolean measurements are removed with the column; down
-- migrations are operator-initiated rollback, not routine operation.

DROP INDEX IF EXISTS ts_telemetry_device_key_time_idx;
ALTER TABLE ts_telemetry DROP COLUMN IF EXISTS v_bool;
