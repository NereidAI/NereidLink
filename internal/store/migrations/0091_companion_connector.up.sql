-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0091_companion_connector.up.sql introduces the platform-neutral companion
-- connector (companion-subscription/v1). NL keeps endpoint registration and
-- subscription intent plus read-back status only: no frame payloads,
-- embeddings, face galleries, persons, ledgers or other companion domain
-- data ever enter these tables. One generic connector serves every
-- conformant companion; a new application is a registered endpoint, not a
-- new NL code path.

-- A registered companion installation, selected by the template's logical
-- companion id plus the target gateway device name. The auth token is
-- deployment secret material; no other secret storage is added here.
CREATE TABLE companion_endpoint (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    companion_id         TEXT NOT NULL,
    gateway_id           TEXT NOT NULL,
    base_url             TEXT NOT NULL,
    contract_version     TEXT NOT NULL DEFAULT 'companion-subscription/v1',
    auth_token           TEXT NOT NULL DEFAULT '',
    insecure_skip_verify BOOLEAN NOT NULL DEFAULT false,
    enabled              BOOLEAN NOT NULL DEFAULT true,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, companion_id, gateway_id)
);

CREATE INDEX companion_endpoint_tenant_idx
    ON companion_endpoint (tenant_id, companion_id, gateway_id);

-- The desired subscription set. Rows are owned per business artifact so
-- re-deploy replaces only that artifact's rows and undeploy removes exactly
-- them; rows of other artifacts sharing the endpoint are untouched.
CREATE TABLE companion_desired_subscription (
    tenant_id            UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    endpoint_id          UUID NOT NULL REFERENCES companion_endpoint(id) ON DELETE CASCADE,
    artifact_key         TEXT NOT NULL,
    row_id               TEXT NOT NULL,
    source_device        TEXT NOT NULL,
    camera               TEXT NOT NULL DEFAULT '',
    pipeline_id          TEXT NOT NULL,
    conclusion_device_id TEXT NOT NULL,
    enabled              BOOLEAN NOT NULL DEFAULT true,
    revision             BIGINT NOT NULL DEFAULT 0,
    status               TEXT NOT NULL DEFAULT 'PENDING'
                             CHECK (status IN ('PENDING','APPLIED','FAILED')),
    reason               TEXT NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, endpoint_id, row_id)
);

CREATE INDEX companion_desired_endpoint_idx
    ON companion_desired_subscription (tenant_id, endpoint_id, source_device);

CREATE INDEX companion_desired_artifact_idx
    ON companion_desired_subscription (tenant_id, artifact_key);

-- Extend the service-account scope whitelist with the companion connector
-- permission points, keeping it in lockstep with rbac.KnownPermissionPoints.
-- The full list is restated (a CHECK cannot be edited incrementally) and
-- matches the migration-0088 baseline plus companion:read/companion:write.
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
        'companion:read', 'companion:write',
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
