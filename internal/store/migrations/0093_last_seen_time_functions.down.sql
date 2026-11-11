-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0093_last_seen_time_functions.down.sql removes the last-seen helper
-- functions. No table data is affected. The application must first be rolled
-- back to a build whose SQL inlines the max(time) subqueries, since releases
-- containing 0093 reference these functions by name.

DROP FUNCTION IF EXISTS last_telemetry_time(uuid);
DROP FUNCTION IF EXISTS last_presence_time(uuid);
