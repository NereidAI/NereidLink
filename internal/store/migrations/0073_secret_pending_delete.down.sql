-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0073_secret_pending_delete.down.sql drops the forced-delete tracking
-- table.
--
-- Data note: a pending row left behind means a deletion was in progress.
-- Dropping the marker makes the vault entry a normal secret again; nothing
-- is deleted by this rollback.

DROP TABLE IF EXISTS tenant_secret_pending_delete;
