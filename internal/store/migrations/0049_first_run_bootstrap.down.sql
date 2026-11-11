-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0049_first_run_bootstrap.down.sql reverses the first-run provisioning while
-- preserving any real configuration. It only removes the bootstrap admin when
-- its password was never set (empty hash); an admin that has already been
-- configured, or any membership/role added by an operator, is left intact.
-- The standard "Default Tenant" is not deleted because it is shared
-- infrastructure typically referenced by device/tenant foreign keys.

-- Membership + role for the deterministic admin.
DELETE FROM membership_role
 WHERE membership_id = '00000000-0000-0000-0000-0000000000a2';

DELETE FROM tenant_membership
 WHERE id = '00000000-0000-0000-0000-0000000000a2';

-- Only an un-bootstrapped admin (empty password) is removed; once a password
-- was set the account is operator data and must survive the rollback.
DELETE FROM app_user
 WHERE id = '00000000-0000-0000-0000-0000000000a1'
   AND password_hash = '';
