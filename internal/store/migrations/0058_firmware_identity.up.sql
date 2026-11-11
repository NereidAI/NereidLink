-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0058_firmware_identity.up.sql gives firmware packages a human-readable
-- identity separate from the device-facing version label and the original
-- file name (ADR-0006d §1):
--   * name is required and is what operators see in lists/selectors;
--   * release_notes is optional free text for a release.
-- Existing rows keep working: name is backfilled from version, then the
-- column is made NOT NULL.

ALTER TABLE firmware_package
    ADD COLUMN IF NOT EXISTS name TEXT;

ALTER TABLE firmware_package
    ADD COLUMN IF NOT EXISTS release_notes TEXT;

UPDATE firmware_package
SET name = version
WHERE name IS NULL;

ALTER TABLE firmware_package
    ALTER COLUMN name SET NOT NULL;
