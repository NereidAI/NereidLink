-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0064_command_catalog_management: drop management columns and
-- return command_catalog to the seed/SQL-only schema from migration 0003.
-- Manually and automatically declared rows both remain as plain entries;
-- only the management metadata is removed.

DROP INDEX IF EXISTS command_catalog_device_idx;
ALTER TABLE command_catalog DROP CONSTRAINT IF EXISTS command_catalog_direction_check;
ALTER TABLE command_catalog
    DROP COLUMN IF EXISTS created_at,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS allow_write,
    DROP COLUMN IF EXISTS allow_read,
    DROP COLUMN IF EXISTS enabled,
    DROP COLUMN IF EXISTS source;
ALTER TABLE command_task DROP COLUMN IF EXISTS direction;
ALTER TABLE command DROP CONSTRAINT IF EXISTS command_direction_check;
ALTER TABLE command DROP COLUMN IF EXISTS direction;
