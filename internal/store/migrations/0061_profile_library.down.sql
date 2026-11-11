-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0061_profile_library.down.sql reverses the tenant profile-library metadata.
-- Dropping the columns also discards edge provenance/addressing, but the core
-- device_profile / measurement_descriptor rows (and operator calibration) are
-- preserved.

DROP TABLE IF EXISTS profile_gateway_source;

ALTER TABLE connector_schema DROP COLUMN IF EXISTS transports;
ALTER TABLE connector_schema DROP COLUMN IF EXISTS protocol;


ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS inactive;
ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS edge_max;
ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS edge_min;
ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS edge_default;
ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS address;
ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS read_write;
ALTER TABLE measurement_descriptor DROP COLUMN IF EXISTS data_type;

DROP INDEX IF EXISTS device_profile_tenant_avail_idx;
ALTER TABLE device_profile DROP COLUMN IF EXISTS availability;
ALTER TABLE device_profile DROP COLUMN IF EXISTS content_hash;
ALTER TABLE device_profile DROP COLUMN IF EXISTS edge_payload;
ALTER TABLE device_profile DROP COLUMN IF EXISTS protocol;
ALTER TABLE device_profile DROP COLUMN IF EXISTS source;
