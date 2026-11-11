-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0066_health_check_interval_ms.up.sql unifies the connector liveness probe
-- contract on a single time unit. health_check previously mixed
-- "interval_s" (seconds) with "timeout_ms" (milliseconds), which made the
-- timeout<interval comparison a cross-unit operation and forced every edge
-- implementation to carry two clocks. The canonical shape is now:
--
--   {
--     "resource":       "camera_status",
--     "interval_ms":    30000,
--     "timeout_ms":     5000,
--     "fail_threshold": 3
--   }
--
-- Existing rows are converted in place (seconds * 1000). Disabled probes
-- are the empty JSON object {} and need no change.
UPDATE connector_instance
   SET health_check = (health_check - 'interval_s')
                          || jsonb_build_object(
                               'interval_ms',
                               (health_check ->> 'interval_s')::int * 1000
                             )
 WHERE health_check ? 'interval_s';
