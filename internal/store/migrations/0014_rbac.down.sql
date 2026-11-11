-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0014: drop RBAC binding tables. Memberships and role
-- assignments are removed; operator-initiated rollback.

DROP TABLE IF EXISTS membership_role;
DROP TABLE IF EXISTS tenant_membership;
