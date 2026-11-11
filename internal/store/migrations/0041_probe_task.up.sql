-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0041_probe_task.up.sql persists on-demand gateway probe tasks and their
-- per-device items (ADR-0026 section 9). An application posts a list of
-- devices to /v1/tenants/{tenant}/probes; NL groups the devices by owning
-- gateway, sends one aggregated downlink frame per gateway on
-- v1/gateway/probe, and correlates the per-device frames returned on
-- v1/gateway/probe/response by probe_id (== task id) and device id.
--
-- The probe result is opaque JSONB: the platform carries no probe semantics.
-- Tasks are short-lived and their results are retained for 24 hours by an
-- explicit sweeper (no long-term value), so there is no history hypertable.

CREATE TABLE IF NOT EXISTS probe_task (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    created_by UUID,
    mode TEXT NOT NULL DEFAULT 'fresh'
        CHECK (mode IN ('cached', 'fresh', 'strict')),
    timeout_ms BIGINT NOT NULL DEFAULT 10000,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'completed', 'partial', 'failed')),
    total_count INTEGER NOT NULL DEFAULT 0,
    succeeded_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

-- The 24h retention sweep deletes terminal tasks by finish time; the tenant
-- index backs the GET-list queries.
CREATE INDEX IF NOT EXISTS probe_task_tenant_idx
    ON probe_task (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS probe_task_retention_idx
    ON probe_task (finished_at)
    WHERE state IN ('completed', 'partial', 'failed');

CREATE TABLE IF NOT EXISTS probe_task_item (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES probe_task(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    gateway_id UUID NOT NULL,
    device_id UUID NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed')),
    result JSONB,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (task_id, device_id)
);

-- The relay resolves pending response correlation by (task, device); the
-- device index also supports per-device cooldown decisions.
CREATE INDEX IF NOT EXISTS probe_task_item_task_idx
    ON probe_task_item (task_id, id);

CREATE INDEX IF NOT EXISTS probe_task_item_device_idx
    ON probe_task_item (tenant_id, device_id, created_at DESC);

CREATE INDEX IF NOT EXISTS probe_task_item_open_idx
    ON probe_task_item (task_id)
    WHERE state IN ('pending', 'running');

-- Extend the migration-0037 service-account scope whitelist to admit the new
-- ADR-0026 probe permission points, keeping this SQL CHECK in lockstep with
-- rbac.KnownPermissionPoints. The constraint is dropped and recreated (CHECK
-- constraints cannot be altered in place).
ALTER TABLE service_account DROP CONSTRAINT service_account_scopes_chk;
ALTER TABLE service_account ADD CONSTRAINT service_account_scopes_chk CHECK (
    scopes <@ ARRAY[
        '*',
        'device:read', 'device:write', 'device:command',
        'credential:read', 'credential:write',
        'profile:read', 'profile:write',
        'rulechain:read', 'rulechain:write',
        'alarm:read', 'alarm:ack', 'alarm:assign', 'alarm:comment',
        'hook:read', 'hook:write',
        'dashboard:read', 'dashboard:write',
        'escalation:read', 'escalation:write',
        'connector:read', 'connector:write',
        'probes:read', 'probes:write',
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
