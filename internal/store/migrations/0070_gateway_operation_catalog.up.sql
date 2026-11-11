-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0070_gateway_operation_catalog.up.sql stores the descriptors for the
-- immediate gateway-level operations a gateway supports (ADR-0035). The
-- kernel carries no built-in operation vocabulary: every operation key, its
-- risk level and its parameter/result schemas are data, advertised by a
-- gateway in hello or defined per tenant.
--
-- Three structures:
--   gateway_operation          global edge-advertised descriptors; the same
--                              gateway software advertises identical rows in
--                              any tenant, so they are stored once.
--   tenant_operation           tenant-defined descriptors (old-gateway
--                              fallback / edited clones of global rows).
--   tenant_operation_disabled  per-tenant disable marks of operation keys.
--
-- The effective catalog per tenant is global rows minus disabled keys plus
-- private rows, which override same keys.

CREATE TABLE IF NOT EXISTS gateway_operation (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    operation     TEXT NOT NULL,
    type_version  TEXT NOT NULL DEFAULT '',
    interaction   TEXT NOT NULL DEFAULT 'request-response'
                  CHECK (interaction IN ('request-response','one-way')),
    timeout_ms    INTEGER NOT NULL DEFAULT 15000
                  CHECK (timeout_ms BETWEEN 100 AND 60000),
    param_schema  JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_schema JSONB NOT NULL DEFAULT '{}'::jsonb,
    risk          TEXT NOT NULL DEFAULT 'normal'
                  CHECK (risk IN ('read','normal','dangerous')),
    display_name  JSONB NOT NULL DEFAULT '{}'::jsonb,
    description   JSONB NOT NULL DEFAULT '{}'::jsonb,
    source        TEXT NOT NULL DEFAULT 'edge' CHECK (source = 'edge'),
    registered_by UUID REFERENCES device(id) ON DELETE SET NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gateway_operation_pk UNIQUE (operation, type_version)
);

CREATE TABLE IF NOT EXISTS tenant_operation (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    operation      TEXT NOT NULL,
    type_version   TEXT NOT NULL DEFAULT '',
    interaction    TEXT NOT NULL DEFAULT 'request-response'
                   CHECK (interaction IN ('request-response','one-way')),
    timeout_ms     INTEGER NOT NULL DEFAULT 15000
                   CHECK (timeout_ms BETWEEN 100 AND 60000),
    param_schema   JSONB NOT NULL DEFAULT '{}'::jsonb,
    result_schema  JSONB NOT NULL DEFAULT '{}'::jsonb,
    risk           TEXT NOT NULL DEFAULT 'normal'
                   CHECK (risk IN ('read','normal','dangerous')),
    display_name   JSONB NOT NULL DEFAULT '{}'::jsonb,
    description    JSONB NOT NULL DEFAULT '{}'::jsonb,
    source         TEXT NOT NULL DEFAULT 'manual' CHECK (source = 'manual'),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tenant_operation_pk
        UNIQUE (tenant_id, operation, type_version)
);

CREATE INDEX IF NOT EXISTS tenant_operation_tenant_idx
    ON tenant_operation (tenant_id);

CREATE TABLE IF NOT EXISTS tenant_operation_disabled (
    tenant_id    UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    operation    TEXT NOT NULL,
    type_version TEXT NOT NULL DEFAULT '',
    disabled_by  UUID REFERENCES app_user(id) ON DELETE SET NULL,
    disabled_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tenant_operation_disabled_pk
        PRIMARY KEY (tenant_id, operation, type_version)
);

-- Extend the service-account scope whitelist with the ADR-0035 points.
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
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
