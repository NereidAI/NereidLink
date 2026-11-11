-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0042: drop connector-instance bindings and the profile connector
-- template columns. The opaque config/mappings already stored on
-- connector_instance are retained, so unbinding the profile loses only the
-- template reference, not previously dispatched desired values.

DROP INDEX IF EXISTS connector_instance_one_per_device;
DROP INDEX IF EXISTS connector_instance_profile_idx;
DROP INDEX IF EXISTS connector_instance_device_idx;

ALTER TABLE connector_instance DROP COLUMN IF EXISTS profile_id;
ALTER TABLE connector_instance DROP COLUMN IF EXISTS device_id;

ALTER TABLE device_profile DROP COLUMN IF EXISTS connector_mappings_template;
ALTER TABLE device_profile DROP COLUMN IF EXISTS connector_config_template;
ALTER TABLE device_profile DROP COLUMN IF EXISTS connector_type_version;
ALTER TABLE device_profile DROP COLUMN IF EXISTS connector_type;
