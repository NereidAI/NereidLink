-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0016 implements offline signed licenses and daily metering from
-- ADR-0024. License payloads are verified with Ed25519 before they can
-- replace the current row. The clock witness singleton participates in
-- the three-witness rollback defense; daily usage rows are partitioned
-- and retained independently from telemetry retention.

ALTER TABLE tenant
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

CREATE TABLE IF NOT EXISTS tenant_license (
    scope TEXT PRIMARY KEY,
    tenant_id UUID,
    customer_name TEXT NOT NULL DEFAULT '',
    device_cap BIGINT NOT NULL,
    tenant_cap BIGINT NOT NULL DEFAULT 0,
    issued_at TIMESTAMPTZ NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    features JSONB NOT NULL DEFAULT '[]'::jsonb,
    payload BYTEA NOT NULL,
    signature BYTEA NOT NULL,
    installed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Scope is '*' for the deployment license, otherwise a tenant UUID.
ALTER TABLE tenant_license
    DROP CONSTRAINT IF EXISTS tenant_license_scope_check;
ALTER TABLE tenant_license
    ADD CONSTRAINT tenant_license_scope_check
    CHECK (scope = '*' OR scope ~ '^[0-9a-fA-F-]{36}$');

CREATE TABLE IF NOT EXISTS license_clock_witness (
    id INTEGER PRIMARY KEY DEFAULT 1,
    last_known_time TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT license_clock_witness_singleton CHECK (id = 1)
);

CREATE TABLE IF NOT EXISTS tenant_usage_daily (
    day DATE NOT NULL,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    device_count INTEGER NOT NULL DEFAULT 0,
    active_device_count INTEGER NOT NULL DEFAULT 0,
    messages BIGINT NOT NULL DEFAULT 0,
    bytes BIGINT NOT NULL DEFAULT 0,
    hot_bytes BIGINT NOT NULL DEFAULT 0,
    warm_bytes BIGINT NOT NULL DEFAULT 0,
    cold_bytes BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (day, tenant_id)
) PARTITION BY RANGE (day);

-- The default partition keeps metering available without annual
-- maintenance. Future operational migrations can detach and split it
-- into dated partitions without losing the fact table.
CREATE TABLE IF NOT EXISTS tenant_usage_daily_default
    PARTITION OF tenant_usage_daily DEFAULT;
