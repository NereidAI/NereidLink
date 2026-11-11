-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0065_connector_health_check.down.sql drops the instance liveness policy.
ALTER TABLE connector_instance DROP COLUMN IF EXISTS health_check;
