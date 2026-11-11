-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0029 by removing gateway identity and binding columns.
-- Existing device rows and their telemetry history are preserved.

DROP INDEX IF EXISTS device_gateway_idx;

ALTER TABLE device
    DROP COLUMN IF EXISTS gateway_id;

ALTER TABLE device
    DROP COLUMN IF NOT EXISTS is_gateway;
