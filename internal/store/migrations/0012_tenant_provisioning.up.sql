-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0012 introduces the outbox-style tenant provisioning flow from
-- ADR-0027 (Q24): creating a tenant and its provisioning task happen in
-- one transaction with status PROVISIONING; the reconciler then builds
-- JetStream topology, the default Profile and the root rule chain
-- idempotently and only then flips the tenant to ACTIVE.
--
-- A non-ACTIVE tenant must not be able to log in or connect devices;
-- enforcement is added with the identity plane. The status column is
-- the single source of truth for that gate.

ALTER TABLE tenant
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'PROVISIONING',
    ADD COLUMN IF NOT EXISTS status_reason TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS provisioned_at TIMESTAMPTZ;

-- Tenants created before this migration were provisioned manually via
-- the seed command, so they are already fully usable.
UPDATE tenant
SET status = 'ACTIVE',
    provisioned_at = COALESCE(provisioned_at, now())
WHERE status = 'PROVISIONING';

-- tenant_provision_task is the outbox row the reconciler claims.
-- FAILED tasks are retried with exponential backoff; attempts and
-- last_error make partial progress observable to operators.
CREATE TABLE IF NOT EXISTS tenant_provision_task (
    tenant_id UUID PRIMARY KEY REFERENCES tenant(id),
    status TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    run_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Backfill ACTIVE tasks for the already provisioned tenants so the
-- task table reflects their lifecycle without triggering another run.
INSERT INTO tenant_provision_task (tenant_id, status, run_after)
SELECT id, 'ACTIVE', now()
FROM tenant
WHERE status = 'ACTIVE'
ON CONFLICT (tenant_id) DO NOTHING;
