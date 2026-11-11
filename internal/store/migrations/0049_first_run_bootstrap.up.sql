-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0049_first_run_bootstrap.up.sql makes a freshly migrated deployment able to
-- create its super administrator password on first login rather than shipping
-- a well-known default password.
--
-- It provisions, idempotently:
--   * the standard "Default Tenant" (the same id `nereidlink seed` uses);
--     NOTE: it is inserted without a status/task and is activated by
--     migration 0050 (ACTIVE + terminal tenant_provision_task). Do not rely on
--     the reconciler, which only claims task rows;
--   * a single platform admin admin@nereidlink.local with an EMPTY
--     password_hash and must_change_password=TRUE when that admin does not
--     exist yet;
--   * a Tenant Admin membership binding that admin to the standard tenant.
--
-- An empty hash means the account cannot log in yet. The first visitor sets
-- the password through the unauthenticated POST /v1/auth/bootstrap endpoint,
-- which fills the hash atomically (conditional on the hash still being
-- empty) and clears must_change_password. Until then the UI shows a setup
-- screen.
--
-- The admin row is keyed by its deterministic id on a new database. On an
-- older database created via `nereidlink seed`, the admin already exists with
-- a different UUID, so the membership lookup resolves the actual id by email
-- (platform admin has tenant_id IS NULL) rather than assuming the fixed id;
-- every write is guarded by NOT EXISTS so re-applying the migration is a
-- no-op.

-- Fixed identifiers used only when the rows do not already exist.
--   admin user:        00000000-0000-0000-0000-0000000000a1
--   tenant membership: 00000000-0000-0000-0000-0000000000a2

INSERT INTO tenant (id, name)
SELECT '00000000-0000-0000-0000-000000000001', 'Default Tenant'
WHERE NOT EXISTS (
    SELECT 1 FROM tenant WHERE id = '00000000-0000-0000-0000-000000000001'
);

INSERT INTO app_user (
    id, tenant_id, email, display_name, password_hash,
    is_platform_admin, email_verified, must_change_password
)
SELECT
    '00000000-0000-0000-0000-0000000000a1',
    NULL,
    'admin@nereidlink.local',
    'Platform Admin',
    '',
    TRUE,
    TRUE,
    TRUE
WHERE NOT EXISTS (
    SELECT 1 FROM app_user WHERE email = 'admin@nereidlink.local'
);

-- Bind whatever platform-admin row now carries the email to the standard
-- tenant (works for both the deterministic fresh id and a legacy seed id).
INSERT INTO tenant_membership (id, user_id, tenant_id, scope)
SELECT
    '00000000-0000-0000-0000-0000000000a2',
    u.id,
    t.id,
    'TENANT'
FROM app_user u
CROSS JOIN tenant t
WHERE u.email = 'admin@nereidlink.local'
  AND u.tenant_id IS NULL
  AND t.id = '00000000-0000-0000-0000-000000000001'
  AND NOT EXISTS (
      SELECT 1 FROM tenant_membership m
      WHERE m.user_id = u.id AND m.tenant_id = t.id
  );

INSERT INTO membership_role (id, membership_id, role)
SELECT gen_random_uuid(), m.id, 'TENANT_ADMIN'
FROM tenant_membership m
WHERE m.id = '00000000-0000-0000-0000-0000000000a2'
  AND NOT EXISTS (
      SELECT 1 FROM membership_role r
      WHERE r.membership_id = m.id AND r.role = 'TENANT_ADMIN'
  );
