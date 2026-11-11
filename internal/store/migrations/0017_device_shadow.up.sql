-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0017 adds the device shadow from ADR-0011. One row holds desired and
-- reported JSON state, independent versions and an ETag for optimistic
-- desired updates. Shadow state is separate from telemetry history and
-- the three device attribute scopes.

CREATE TABLE IF NOT EXISTS device_shadow (
    device_id UUID PRIMARY KEY REFERENCES device(id) ON DELETE CASCADE,
    desired JSONB NOT NULL DEFAULT '{}'::jsonb,
    reported JSONB NOT NULL DEFAULT '{}'::jsonb,
    desired_version BIGINT NOT NULL DEFAULT 0,
    reported_version BIGINT NOT NULL DEFAULT 0,
    desired_etag TEXT NOT NULL DEFAULT '"0"',
    desired_at TIMESTAMPTZ,
    reported_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Shadow change history follows the attribute time-series pattern.
CREATE TABLE IF NOT EXISTS ts_shadow_event (
    time TIMESTAMPTZ NOT NULL,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    phase TEXT NOT NULL CHECK (phase IN ('desired', 'reported')),
    version BIGINT NOT NULL,
    state JSONB NOT NULL
);

SELECT create_hypertable('ts_shadow_event', 'time', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS ts_shadow_event_device_time_idx
    ON ts_shadow_event (device_id, time DESC);
