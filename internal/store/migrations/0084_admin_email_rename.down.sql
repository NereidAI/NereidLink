-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0084_admin_email_rename.down.sql restores the original admin email.

UPDATE app_user
SET email = 'admin@nereidlink.local'
WHERE email = 'admin@nereidai.local'
  AND tenant_id IS NULL
  AND is_platform_admin = TRUE;