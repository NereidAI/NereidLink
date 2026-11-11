-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0048_ota_task.up.sql persists firmware OTA tasks and their per-device
-- progress (ADR-0026 edge-plane deferred slice). An application posts a
-- firmware descriptor (version, size, mandatory sha256, an operator-supplied
-- download URL and optional HTTP headers) plus a list of target sub-devices;
-- NL groups the targets by owning gateway, sends one v1/gateway/ota/task
-- downlink frame per gateway, and correlates the v1/gateway/ota/progress
-- frames the gateways report by task id and device id.
--
-- The platform never stores or relays the firmware blob: the edge fetches it
-- from the supplied HTTPS URL and enforces sha256 before applying (the S3
-- HTTPS relay of ADR-0006 remains a separate, not-yet-built slice; so does
-- the edge-agent self-upgrade task type).
--
-- OTA tasks are long-running operational records (a rollout may take hours or
-- days), so unlike probe tasks they are not auto-purged on a 24h timer.

CREATE TABLE IF NOT EXISTS ota_task (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    created_by UUID,
    firmware_version TEXT NOT NULL,
    package_url TEXT NOT NULL,
    package_sha256 TEXT NOT NULL,
    package_size BIGINT NOT NULL DEFAULT 0
        CHECK (package_size >= 0),
    http_headers JSONB NOT NULL DEFAULT '{}'::jsonb,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'running', 'completed', 'partial', 'failed', 'canceled')),
    total_count INTEGER NOT NULL DEFAULT 0,
    succeeded_count INTEGER NOT NULL DEFAULT 0,
    failed_count INTEGER NOT NULL DEFAULT 0,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

-- Tenant list queries are newest-first.
CREATE INDEX IF NOT EXISTS ota_task_tenant_idx
    ON ota_task (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS ota_task_open_idx
    ON ota_task (state)
    WHERE state IN ('pending', 'running');

CREATE TABLE IF NOT EXISTS ota_task_item (
    id UUID PRIMARY KEY,
    task_id UUID NOT NULL REFERENCES ota_task(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL,
    gateway_id UUID NOT NULL,
    device_id UUID NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'downloading', 'downloaded', 'updating',
                         'succeeded', 'failed')),
    progress_pct INTEGER NOT NULL DEFAULT 0
        CHECK (progress_pct BETWEEN 0 AND 100),
    message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at TIMESTAMPTZ,
    UNIQUE (task_id, device_id)
);

-- The relay correlates a progress frame by (task, device); the open index
-- backs startup recovery and the deployment-time backstop.
CREATE INDEX IF NOT EXISTS ota_task_item_task_idx
    ON ota_task_item (task_id, id);

CREATE INDEX IF NOT EXISTS ota_task_item_open_idx
    ON ota_task_item (task_id)
    WHERE state IN ('pending', 'downloading', 'downloaded', 'updating');

CREATE INDEX IF NOT EXISTS ota_task_item_device_idx
    ON ota_task_item (tenant_id, device_id, created_at DESC);

-- Maps a progress state to a forward-only ordinal so a late frame cannot
-- move an item backwards (e.g. "updating" -> "downloading"). Terminal states
-- are not ordered by the UPDATE path, but are given ordinals for completeness.
CREATE OR REPLACE FUNCTION ota_progress_order(state TEXT) RETURNS INTEGER AS $$
    SELECT CASE state
        WHEN 'pending' THEN 0
        WHEN 'downloading' THEN 1
        WHEN 'downloaded' THEN 2
        WHEN 'updating' THEN 3
        WHEN 'succeeded' THEN 4
        WHEN 'failed' THEN 4
        ELSE 0
    END;
$$ LANGUAGE sql IMMUTABLE;
