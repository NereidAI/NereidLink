-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- storage_hook declares ordered pre-storage user hooks. Hooks execute in
-- the processing plane after JetStream consumption and immediately before
-- Timescale persistence; they never run on the connection plane.
CREATE TABLE IF NOT EXISTS storage_hook (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    hook_order INTEGER NOT NULL DEFAULT 0,
    hook_type TEXT NOT NULL CHECK (hook_type IN ('expression', 'webhook')),
    -- An empty array means the hook applies to every measurement key.
    measurement_keys TEXT[] NOT NULL DEFAULT '{}',
    -- Type-specific configuration:
    --   expression: {"script": "..."}
    --   webhook:    {"url": "http://..."}
    config JSONB NOT NULL DEFAULT '{}',
    timeout_ms INTEGER NOT NULL DEFAULT 1000 CHECK (timeout_ms > 0),
    failure_policy TEXT NOT NULL DEFAULT 'passthrough' CHECK (
        failure_policy IN ('passthrough', 'abort', 'dlq')
    ),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS storage_hook_tenant_idx
    ON storage_hook (tenant_id);

-- telemetry_dlq receives rows whose hook failed with the dlq policy. The
-- table is intentionally plain (not a hypertable): hook failures are rare
-- operational events, queried for diagnostics only.
CREATE TABLE IF NOT EXISTS telemetry_dlq (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    time TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    measurement_key TEXT NOT NULL,
    point JSONB NOT NULL,
    hook_name TEXT NOT NULL,
    reason TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS telemetry_dlq_time_idx
    ON telemetry_dlq (time DESC);
