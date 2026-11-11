-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0074_forwarder.up.sql introduces third-party data forwarding (ADR-0018).
-- The data path is fully asynchronous: rule-engine "forward" nodes publish a
-- fire-and-forget envelope onto one JetStream stream per forwarder
-- (nl.egress.<tenant>.<forwarder>), and an in-process worker delivers it to
-- the configured sink. These two tables hold only:
--   * forwarder      - the per-tenant forwarder configuration (sink type,
--                      sink parameters and retry policy).
--   * forwarder_dlq  - envelopes that exhausted retries or failed a terminal
--                      check, with the last error and attempt count, so an
--                      operator can inspect and manually replay them.
--
-- Isolation invariant: a faulty third-party receiver only fills its own
-- bounded JetStream backlog; nothing in these tables or the sink calls is on
-- the telemetry/ingest hot path, so an external outage can never block NL.

CREATE TABLE IF NOT EXISTS forwarder (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    name TEXT NOT NULL,
    sink_type TEXT NOT NULL,
    sink_config JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Slice 1 ships the JSON encoder only; more encoders are added later.
    encoder TEXT NOT NULL DEFAULT 'json'
        CHECK (encoder IN ('json')),
    -- Delivery reliability tuning; all bounds are explicit.
    max_attempts INTEGER NOT NULL DEFAULT 5
        CHECK (max_attempts BETWEEN 1 AND 50),
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS forwarder_tenant_idx
    ON forwarder (tenant_id, name);

CREATE TABLE IF NOT EXISTS forwarder_dlq (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    forwarder_id UUID NOT NULL REFERENCES forwarder(id) ON DELETE CASCADE,
    -- The original envelope is stored verbatim so a replay re-publishes it.
    payload JSONB NOT NULL,
    -- Failure axis and last attempt metadata for triage.
    error TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    last_tried_at TIMESTAMPTZ,
    -- A non-null replayed_at records a successful manual replay.
    replayed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS forwarder_dlq_tenant_idx
    ON forwarder_dlq (tenant_id, created_at DESC);

CREATE INDEX IF NOT EXISTS forwarder_dlq_forwarder_idx
    ON forwarder_dlq (forwarder_id, created_at DESC);

-- Extend the service-account scope whitelist with the new forwarder points,
-- keeping it in lockstep with rbac.KnownPermissionPoints. The full list is
-- restated (a CHECK cannot be edited incrementally).
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
        'edge_secret:read', 'edge_secret:write',
        'edge_operation:read', 'edge_operation:exec',
        'forwarder:read', 'forwarder:write',
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
