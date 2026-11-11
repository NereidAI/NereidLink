-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0085_profile_downlink.down.sql reverses the profile downlink tables and the
-- content-revision columns added by the up migration.
DROP TABLE IF EXISTS profile_delivery;
DROP TABLE IF EXISTS profile_gateway_target;
ALTER TABLE device DROP COLUMN IF EXISTS agent_profile_formats;
ALTER TABLE device_profile DROP COLUMN IF EXISTS desired_hash;
ALTER TABLE device_profile DROP COLUMN IF EXISTS revision;
