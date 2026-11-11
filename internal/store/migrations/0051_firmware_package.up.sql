-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0051_firmware_package.up.sql introduces platform-hosted firmware OTA
-- (ADR-0006b). The platform now stores firmware binaries in the configured
-- object store (S3/SeaweedFS) and keeps only metadata here; the bytes never
-- live in PostgreSQL. Devices/gateways fetch a package through the
-- authenticated NL HTTPS relay (GET /v1/me/ota/{task}/package) rather than a
-- public presigned URL, because firmware is sensitive and authorized per
-- device.
--
-- Data-preservation notes:
--   * firmware_package rows are operational metadata. Deleting the rows does
--     not delete the blobs automatically; the service removes both together.
--   * device.firmware_version is the last version the device reported, and is
--     also written back by the platform after a successful task.
--   * ota_task.firmware_id links a task to a repository package; the legacy
--     external-URL flow stays valid with firmware_id NULL.
--   * ota_task_item.gateway_id becomes nullable so directly connected
--     MQTT/HTTP devices (gateway_id IS NULL) can be OTA targets too.
--   * the item state check gains 'skipped', already emitted by the manager
--     for targets that could not be dispatched.

CREATE TABLE IF NOT EXISTS firmware_package (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    -- NULL marks a profile-agnostic package; otherwise the firmware is only
    -- compatible with this device profile/model.
    profile_id UUID REFERENCES device_profile(id) ON DELETE SET NULL,
    version TEXT NOT NULL,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'application/octet-stream',
    object_key TEXT NOT NULL,
    size_bytes BIGINT NOT NULL DEFAULT 0
        CHECK (size_bytes >= 0),
    sha256 CHAR(64) NOT NULL,
    is_stable BOOLEAN NOT NULL DEFAULT false,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Profile-scoped packages are unique per version; a package for a different
-- model may reuse the same version label.
CREATE UNIQUE INDEX IF NOT EXISTS firmware_pkg_profile_version_uniq
    ON firmware_package (tenant_id, profile_id, version)
    WHERE profile_id IS NOT NULL;

-- Profile-agnostic packages are unique per (tenant, version).
CREATE UNIQUE INDEX IF NOT EXISTS firmware_pkg_generic_version_uniq
    ON firmware_package (tenant_id, version)
    WHERE profile_id IS NULL;

CREATE INDEX IF NOT EXISTS firmware_pkg_tenant_idx
    ON firmware_package (tenant_id, created_at DESC);

-- Last firmware version the device reported / was rolled out to.
ALTER TABLE device
    ADD COLUMN IF NOT EXISTS firmware_version TEXT;

-- Link a repository-managed task to its package; external-URL tasks leave it
-- NULL so the pre-0051 flow keeps working.
ALTER TABLE ota_task
    ADD COLUMN IF NOT EXISTS firmware_id UUID REFERENCES firmware_package(id);

-- Direct devices are OTA targets without a owning gateway.
ALTER TABLE ota_task_item
    ALTER COLUMN gateway_id DROP NOT NULL;

-- Recreate the state check to include the manager-emitted 'skipped' state.
ALTER TABLE ota_task_item
    DROP CONSTRAINT IF EXISTS ota_task_item_state_check;
ALTER TABLE ota_task_item
    ADD CONSTRAINT ota_task_item_state_check
    CHECK (state IN ('pending', 'downloading', 'downloaded', 'updating',
                     'succeeded', 'failed', 'skipped'));
