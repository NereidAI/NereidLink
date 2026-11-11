-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0050_default_tenant_active.down.sql intentionally does not revert the
-- Default Tenant to PROVISIONING. Doing so would immediately hide it from the
-- tenant list and block all of its device credentials, even if devices,
-- profiles and operator data were created after activation. The up migration
-- only repairs the first-run lifecycle marker; rollback therefore leaves
-- operational data intact.

SELECT 1;
