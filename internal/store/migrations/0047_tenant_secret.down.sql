-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0047_tenant_secret.down.sql removes the per-tenant secret vault. The
-- connector specs keep their secret:// references, which then fail to resolve
-- at the downlink last mile (empty value), but no device data is removed.

DROP INDEX IF EXISTS tenant_secret_tenant_idx;
DROP TABLE IF EXISTS tenant_secret;
