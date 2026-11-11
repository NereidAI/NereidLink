-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Rule chains, nodes and edges. A chain is immutable per version: editing
-- creates a new version row and activation flips the single active pointer
-- per tenant. At most one root chain exists per tenant in the MVP.

CREATE TABLE IF NOT EXISTS rule_chain (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    name TEXT NOT NULL,
    version INTEGER NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
    is_root BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name, version)
);

CREATE TABLE IF NOT EXISTS rule_node (
    id UUID PRIMARY KEY,
    chain_id UUID NOT NULL REFERENCES rule_chain(id) ON DELETE CASCADE,
    type TEXT NOT NULL,
    name TEXT NOT NULL,
    config JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE TABLE IF NOT EXISTS rule_edge (
    id UUID PRIMARY KEY,
    chain_id UUID NOT NULL REFERENCES rule_chain(id) ON DELETE CASCADE,
    from_node UUID NOT NULL REFERENCES rule_node(id) ON DELETE CASCADE,
    to_node UUID NOT NULL REFERENCES rule_node(id) ON DELETE CASCADE,
    relation TEXT NOT NULL
);

-- Device attributes written by the Save Attributes node. Scope follows the
-- device shadow model (ADR-0011); the MVP chain only writes CLIENT scope.

CREATE TABLE IF NOT EXISTS device_attribute (
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    scope TEXT NOT NULL CHECK (scope IN ('CLIENT', 'SERVER_SCOPE', 'SHARED_SCOPE')),
    key TEXT NOT NULL,
    value JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, device_id, scope, key)
);

-- Alarms with lifecycle state and dedupe merging. A live alarm is ACTIVE or
-- ACK; clearing sets CLEARED. The dedupe key uniquely identifies the open
-- alarm, but uniqueness must only hold among live rows, enforced by a
-- partial unique index.

CREATE TABLE IF NOT EXISTS alarm (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    rule_id UUID,
    type TEXT NOT NULL,
    severity TEXT NOT NULL CHECK (severity IN ('CRITICAL', 'MAJOR', 'MINOR', 'WARNING')),
    state TEXT NOT NULL CHECK (state IN ('ACTIVE', 'ACK', 'CLEARED')),
    dedupe_key TEXT NOT NULL,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    propagate BOOLEAN NOT NULL DEFAULT FALSE,
    started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    acked_at TIMESTAMPTZ,
    cleared_at TIMESTAMPTZ,
    ended_at TIMESTAMPTZ
);

CREATE UNIQUE INDEX IF NOT EXISTS alarm_live_dedupe_idx
    ON alarm (tenant_id, dedupe_key)
    WHERE state IN ('ACTIVE', 'ACK');

CREATE INDEX IF NOT EXISTS alarm_device_time_idx
    ON alarm (tenant_id, device_id, started_at DESC);

CREATE INDEX IF NOT EXISTS alarm_state_idx
    ON alarm (tenant_id, state, started_at DESC);
