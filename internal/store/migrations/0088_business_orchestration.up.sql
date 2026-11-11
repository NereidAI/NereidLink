-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0088_business_orchestration.up.sql adds versioned business-assembly
-- documents and deterministic expansion snapshots. It stores configuration
-- intent only: no frame payloads, embeddings, face galleries, meter ledgers,
-- scripts, or companion-service records belong in these tables.

-- Custom template versions are immutable. Built-in templates are supplied by
-- the business module and therefore need no per-tenant seed rows.
CREATE TABLE business_template (
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    id          TEXT NOT NULL,
    version     TEXT NOT NULL,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    document    JSONB NOT NULL,
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, id, version)
);

CREATE INDEX business_template_tenant_idx
    ON business_template (tenant_id, id, version);

-- An instance owns its original template document so later template releases
-- cannot silently change an existing deployment. generation fences desired
-- updates; active_generation identifies the last generation that passed
-- health read-back and is the rollback target after a failed update.
CREATE TABLE business_instance (
    id                UUID PRIMARY KEY,
    tenant_id         UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    template_id       TEXT NOT NULL,
    template_version  TEXT NOT NULL,
    template_document JSONB NOT NULL,
    parameters        JSONB NOT NULL DEFAULT '{}'::jsonb,
    assembly          JSONB NOT NULL,
    status            TEXT NOT NULL DEFAULT 'DRAFT'
                          CHECK (status IN ('DRAFT','VALIDATING','DEPLOYING',
                                            'RUNNING','DEGRADED','FAILED',
                                            'UNDEPLOYED')),
    generation        BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    active_generation BIGINT NOT NULL DEFAULT 0 CHECK (active_generation >= 0),
    pending_generation BIGINT NOT NULL DEFAULT 0 CHECK (pending_generation >= 0),
    validation        JSONB NOT NULL DEFAULT '{"valid":false,"issues":[]}'::jsonb,
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    deployed_at       TIMESTAMPTZ,
    UNIQUE (tenant_id, name)
);

CREATE INDEX business_instance_tenant_idx
    ON business_instance (tenant_id, updated_at DESC);
CREATE INDEX business_instance_status_idx
    ON business_instance (tenant_id, status);

-- Every desired generation has an immutable expansion document and digest.
-- resources records owner-specific ids returned during apply; it contains no
-- secrets. Keeping every snapshot makes retry, rollback and audit independent
-- of the current template market contents.
CREATE TABLE business_expansion (
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    instance_id UUID NOT NULL REFERENCES business_instance(id) ON DELETE CASCADE,
    generation  BIGINT NOT NULL CHECK (generation > 0),
    digest      TEXT NOT NULL,
    document    JSONB NOT NULL,
    resources   JSONB NOT NULL DEFAULT '[]'::jsonb,
    status      TEXT NOT NULL DEFAULT 'DRAFT'
                    CHECK (status IN ('DRAFT','DEPLOYING','ACTIVE','FAILED',
                                      'UNDEPLOYED')),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, instance_id, generation)
);

CREATE INDEX business_expansion_instance_idx
    ON business_expansion (instance_id, generation DESC);

-- Extend the machine-scope whitelist in the same migration as the new route
-- permission points. Existing grants are preserved verbatim.
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
        'business:read', 'business:write',
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
