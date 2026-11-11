-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0040_connector_instance.up.sql persists the desired connector spec and the
-- last reported connector state for one NereidEdge gateway (ADR-0026). NL is
-- the system of record for desired; the edge reports APPLIED/FAILED/DEGRADED
-- plus the revision it applied. A spec row therefore carries two halves:
-- the desired spec (type/version/config/mappings/revision) and the reported
-- status (state/revision/reason/timestamp).
--
-- The connector config and point mappings are opaque JSONB: the NL kernel
-- never interprets protocol-specific config (new connector types must not
-- require an NL upgrade). JSON-schema validation against an edge-registered
-- schema is a later slice.
--
-- Deleting/unbinding a gateway device removes its connector specs through
-- the ON DELETE CASCADE foreign keys, matching ADR-0026 "unbind deletes
-- configuration, which is non-business data".

CREATE TABLE connector_instance (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id       UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,

    -- Desired half.
    connector_type   TEXT NOT NULL,
    type_version     TEXT NOT NULL DEFAULT '',
    config           JSONB NOT NULL DEFAULT '{}'::jsonb,
    mappings         JSONB NOT NULL DEFAULT '[]'::jsonb,
    revision         BIGINT NOT NULL DEFAULT 1,

    -- Lifecycle / reported half.
    state            TEXT NOT NULL DEFAULT 'PENDING'
                         CHECK (state IN ('PENDING','DISPATCHED','CREATING',
                                          'APPLIED','FAILED','DEGRADED')),
    reported_revision BIGINT NOT NULL DEFAULT 0,
    report_reason    TEXT NOT NULL DEFAULT '',
    not_applied_alarm BOOLEAN NOT NULL DEFAULT FALSE,
    failed_alarm     BOOLEAN NOT NULL DEFAULT FALSE,
    failed_critical  BOOLEAN NOT NULL DEFAULT FALSE,
    failed_at        TIMESTAMPTZ,

    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    desired_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at    TIMESTAMPTZ,
    reported_at      TIMESTAMPTZ
);

-- Fleet and gateway views list specs by gateway; id is the globally unique
-- spec_id delivered to and echoed back by the edge.
CREATE INDEX connector_instance_gateway_idx
    ON connector_instance (gateway_id, created_at);

CREATE INDEX connector_instance_tenant_gateway_idx
    ON connector_instance (tenant_id, gateway_id);

-- The not-applied sweep scans open (never APPLIED) specs by desired age.
CREATE INDEX connector_instance_open_idx
    ON connector_instance (desired_at)
    WHERE state IN ('PENDING','DISPATCHED','CREATING');

-- Extend the migration-0037 service-account scope whitelist to admit the new
-- ADR-0026 connector permission points, keeping this SQL CHECK in lockstep
-- with rbac.KnownPermissionPoints. The constraint is dropped and recreated
-- (CHECK constraints cannot be altered in place).
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
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
