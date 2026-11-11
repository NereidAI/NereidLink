-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0056_gateway_edge_profile.down.sql drops the edge-reported EdgeX profile
-- cache. Native device profiles and measurement descriptors created by adopt
-- are left untouched.

DROP TABLE IF EXISTS gateway_edge_profile;
