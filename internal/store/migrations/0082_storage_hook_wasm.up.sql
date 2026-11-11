-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0082 adds the WASM hook mechanism (ADR-0015): a storage hook may be a
-- wazero-sandboxed WebAssembly module in addition to the existing goja
-- expression and external webhook. PostgreSQL cannot alter a CHECK in place,
-- so the generated type constraint is dropped and recreated with the widened
-- value set. The original constraint name (storage_hook_hook_type_check) is
-- the name Postgres assigns to the table-level CHECK from migration 0006; the
-- IF EXISTS keeps this idempotent if it was already renamed.

ALTER TABLE storage_hook
    DROP CONSTRAINT IF EXISTS storage_hook_hook_type_check;

ALTER TABLE storage_hook
    DROP CONSTRAINT IF EXISTS storage_hook_type_check;

ALTER TABLE storage_hook
    ADD CONSTRAINT storage_hook_type_check
        CHECK (hook_type IN ('expression', 'webhook', 'wasm'));
