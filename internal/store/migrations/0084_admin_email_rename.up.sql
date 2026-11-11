-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0084_admin_email_rename.up.sql renames the deterministic platform
-- administrator email from admin@nereidlink.local to admin@nereidai.local so
-- that SSO (Keycloak realm nereidai) identity matches the local account
-- (JIT provisioning in UpsertExternalUser matches by global email).

UPDATE app_user
SET email = 'admin@nereidai.local'
WHERE email = 'admin@nereidlink.local'
  AND tenant_id IS NULL
  AND is_platform_admin = TRUE;