-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0064_command_catalog_management.up.sql turns command_catalog from a
-- seed/SQL-only table into the operator-managed per-device command
-- whitelist described in the command-direction design: every dispatched
-- name must be declared here, entries carry an allowed read/write
-- direction mask, a provenance marker (manual declarations vs entries
-- materialized from the bound profile's readable points), an enable
-- switch, an optional description and a creation time.
--
-- Forward-compatible defaults:
--   * All pre-existing rows (seed/dev entries) are treated as explicitly
--     operator-authored: source='manual', enabled=true.
--   * Because older rows could drive either reads or writes on the edge,
--     both allow_read and allow_write default to true; newly created
--     entries require at least one direction explicitly.
--   * The catalog stays per-device (PK tenant_id, device_id, name).
--
-- Direction semantics:
--   * allow_read  - the name may be dispatched as a read (edge performs
--                   the south-side GET/equivalent, value returned).
--   * allow_write - the name may be dispatched as a write (side effects;
--                   the params document is meaningful).
--   At least one must be true.

ALTER TABLE command_catalog
    ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'manual'
        CHECK (source IN ('auto', 'manual')),
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS allow_read BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS allow_write BOOLEAN NOT NULL DEFAULT true,
    ADD COLUMN IF NOT EXISTS description TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- At least one direction must be allowed. The boolean defaults keep
-- every pre-existing row valid, so the scan is trivial.
ALTER TABLE command_catalog
    ADD CONSTRAINT command_catalog_direction_check
    CHECK (allow_read OR allow_write);

-- Listing one device's declared commands newest-first is the dominant
-- console query.
CREATE INDEX IF NOT EXISTS command_catalog_device_idx
    ON command_catalog (tenant_id, device_id, created_at DESC);

-- Per-command direction audit: every dispatched row records whether the
-- edge was asked to read or write. Historical rows stay empty (legacy,
-- direction was inferred on the edge).
ALTER TABLE command
    ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT ''
        CHECK (direction IN ('', 'read', 'write'));

-- Batch tasks carry the same direction so every item resolves the same
-- south-side operation.
ALTER TABLE command_task
    ADD COLUMN IF NOT EXISTS direction TEXT NOT NULL DEFAULT ''
        CHECK (direction IN ('', 'read', 'write'));
