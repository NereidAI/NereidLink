-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0050_default_tenant_active.up.sql repairs the first-run Default Tenant
-- created by migration 0049.
--
-- 0049 inserted the standard tenant row with only (id, name), so it inherited
-- the 0012 column default status='PROVISIONING' and got no
-- tenant_provision_task row. The provisioning reconciler only claims task
-- rows, so nothing ever flipped the tenant to ACTIVE and it did not appear in
-- the platform tenant list (which INNER JOINs the task table). More
-- importantly, device credential lookup requires tenant status='ACTIVE', so
-- every credential under the first-run tenant was rejected at connect time
-- ("unknown or inactive credential" / MQTT CONNACK bad username or password).
--
-- The Default Tenant is fully provisioned by definition (it is the same
-- standard tenant `nereidlink seed` creates), so activate it directly and
-- record a terminal ACTIVE task row. Both statements are idempotent and are
-- scoped to the deterministic standard tenant id, so normally provisioned
-- tenants (which carry their own task lifecycle) are untouched.

UPDATE tenant
SET status = 'ACTIVE',
    status_reason = '',
    provisioned_at = COALESCE(provisioned_at, now()),
    updated_at = now()
WHERE id = '00000000-0000-0000-0000-000000000001'
  AND status <> 'ACTIVE';

INSERT INTO tenant_provision_task
    (tenant_id, status, attempts, last_error, run_after, created_at, updated_at)
SELECT id, 'ACTIVE', 0, '', now(), now(), now()
FROM tenant
WHERE id = '00000000-0000-0000-0000-000000000001'
ON CONFLICT (tenant_id) DO UPDATE
SET status = 'ACTIVE',
    last_error = '',
    run_after = now(),
    updated_at = now();
