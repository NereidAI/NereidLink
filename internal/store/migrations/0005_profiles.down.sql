-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

ALTER TABLE device DROP COLUMN IF EXISTS profile_id;
ALTER TABLE ts_telemetry DROP COLUMN IF EXISTS unit;
ALTER TABLE ts_telemetry DROP COLUMN IF EXISTS raw_value;
ALTER TABLE ts_telemetry DROP COLUMN IF EXISTS descriptor_version;
ALTER TABLE ts_telemetry DROP COLUMN IF EXISTS v_string;
DROP TABLE IF EXISTS device_measurement_override;
DROP TABLE IF EXISTS measurement_descriptor;
DROP TABLE IF EXISTS device_profile;
