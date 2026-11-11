-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0042_connector_template.up.sql introduces the two-level connector
-- configuration model from ADR-0026: a device profile may carry a connector
-- TEMPLATE (type/version plus default config and point mappings), and a
-- connector instance may bind to one south-bound sub-device plus one
-- profile. At dispatch time NL deep-merges the profile template with the
-- instance's per-device overrides; the kernel still never interprets the
-- opaque JSON contents.
--
-- All new columns are nullable / defaulted so existing profiles and
-- connector instances keep their 0040/0041 behaviour unchanged: a spec
-- without a profile_id is delivered with its own config/mappings verbatim.

-- 1. Connector template on the device profile. connector_type being empty
--    means the profile carries telemetry descriptors only (the 0005 model).
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS connector_type
    TEXT NOT NULL DEFAULT '';
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS connector_type_version
    TEXT NOT NULL DEFAULT '';
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS connector_config_template
    JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS connector_mappings_template
    JSONB NOT NULL DEFAULT '[]'::jsonb;

-- 2. Optional sub-device binding and profile reference on a connector
--    instance. Removing the sub-device deletes its connector config
--    (non-business data, ON DELETE CASCADE); removing the profile detaches
--    the template while the instance keeps its own stored overrides
--    (ON DELETE SET NULL).
ALTER TABLE connector_instance ADD COLUMN IF NOT EXISTS device_id
    UUID REFERENCES device(id) ON DELETE CASCADE;
ALTER TABLE connector_instance ADD COLUMN IF NOT EXISTS profile_id
    UUID REFERENCES device_profile(id) ON DELETE SET NULL;

-- Bindings are resolved per gateway/sub-device and listed when a profile's
-- template is re-synced, so index both foreign keys.
CREATE INDEX IF NOT EXISTS connector_instance_device_idx
    ON connector_instance (device_id) WHERE device_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS connector_instance_profile_idx
    ON connector_instance (profile_id) WHERE profile_id IS NOT NULL;

-- At most one connector instance per bound sub-device. A gateway-level
-- instance (device_id NULL) is unrestricted. This matches the EdgeX model
-- where one physical south-bound device is served through one connector
-- instance, and keeps re-POSTing the same binding idempotent at the data
-- layer instead of producing duplicate specs.
CREATE UNIQUE INDEX IF NOT EXISTS connector_instance_one_per_device
    ON connector_instance (tenant_id, device_id)
    WHERE device_id IS NOT NULL;
