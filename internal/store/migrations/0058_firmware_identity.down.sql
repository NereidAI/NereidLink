-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0058_firmware_identity.down.sql drops the release-identity columns.
ALTER TABLE firmware_package
    DROP COLUMN IF EXISTS release_notes;

ALTER TABLE firmware_package
    DROP COLUMN IF EXISTS name;
