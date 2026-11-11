-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Command audit and delivery table. The state column is the authoritative
-- lifecycle record; pending persistent commands are recovered from here
-- after router or node crashes.

CREATE TABLE IF NOT EXISTS command (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    name TEXT NOT NULL,
    params JSONB NOT NULL DEFAULT '{}'::jsonb,
    interaction TEXT NOT NULL CHECK (interaction IN ('one-way', 'request-response')),
    timeliness TEXT NOT NULL CHECK (timeliness IN ('realtime', 'persistent')),
    timeout_ms BIGINT NOT NULL DEFAULT 0,
    state TEXT NOT NULL,
    response JSONB,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    claimed_by TEXT,
    claimed_at TIMESTAMPTZ,
    delivered_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS command_device_state_idx
    ON command (tenant_id, device_id, state, created_at);

CREATE INDEX IF NOT EXISTS command_expiry_idx
    ON command (timeliness, state, expires_at);

-- Command Catalog: per device, declared commands with interaction,
-- timeliness and default timeout. Undeclared commands are rejected.

CREATE TABLE IF NOT EXISTS command_catalog (
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    name TEXT NOT NULL,
    interaction TEXT NOT NULL CHECK (interaction IN ('one-way', 'request-response')),
    timeliness TEXT NOT NULL CHECK (timeliness IN ('realtime', 'persistent')),
    default_timeout_ms BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_id, device_id, name)
);
