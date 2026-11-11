-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back 0082: restore the expression/webhook-only CHECK. This fails if a
-- wasm hook still exists; delete or convert those rows before migrating down.

ALTER TABLE storage_hook
    DROP CONSTRAINT IF EXISTS storage_hook_type_check;

ALTER TABLE storage_hook
    ADD CONSTRAINT storage_hook_hook_type_check
        CHECK (hook_type IN ('expression', 'webhook'));
