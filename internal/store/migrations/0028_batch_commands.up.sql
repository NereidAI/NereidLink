-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0028 adds asynchronous batch command tasks (ADR-0012). A task expands
-- either a device-group target or an explicit device-ID selector into one
-- independent command per device. Existing commands remain untouched and
-- failed items never roll back deliveries already accepted by devices.

CREATE TABLE IF NOT EXISTS command_task (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    created_by UUID,
    name TEXT NOT NULL,
    params JSONB NOT NULL DEFAULT '{}'::jsonb,
    timeout_ms BIGINT NOT NULL DEFAULT 0,
    target_type TEXT NOT NULL CHECK (target_type IN ('group', 'devices')),
    group_id UUID,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'completed', 'partial', 'failed')),
    total_count INTEGER NOT NULL DEFAULT 0,
    succeeded_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    CONSTRAINT command_task_target_check CHECK (
        (target_type = 'group' AND group_id IS NOT NULL)
        OR (target_type = 'devices' AND group_id IS NULL)
    )
);

CREATE INDEX IF NOT EXISTS command_task_tenant_idx
    ON command_task (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS command_task_state_idx
    ON command_task (state);

CREATE TABLE IF NOT EXISTS command_task_item (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES command_task(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    command_id UUID,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'succeeded', 'failed')),
    response JSONB,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (task_id, device_id)
);

CREATE INDEX IF NOT EXISTS command_task_item_task_idx
    ON command_task_item (task_id, id);

CREATE INDEX IF NOT EXISTS command_task_item_device_idx
    ON command_task_item (tenant_id, device_id, created_at DESC);
