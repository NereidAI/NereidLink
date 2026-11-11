-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0067_edge_secret_type.up.sql stores the descriptors of credentials the
-- platform delivers to gateways (ADR-0034 "edge secret" type catalog). The
-- kernel itself understands no credential protocol (no ONVIF or similar
-- vocabulary): a descriptor only declares which fields a credential of this
-- type carries, as a self-contained draft 2020-12 JSON Schema, plus optional
-- bilingual display names. Credential values later stored in tenant_secret
-- are validated against it on a best-effort basis.
--
-- Two sources share the table:
--   * source='edge'   a negotiated gateway advertises the descriptor in
--                      hello; re-registering on reconnect upserts it.
--   * source='manual' a tenant operator creates it for gateways that do not
--                      advertise (or clones an edge descriptor to edit).
-- Both are tenant-scoped: the same edge software registers independently in
-- every tenant that runs it and rows cascade with the tenant.

CREATE TABLE IF NOT EXISTS edge_secret_type (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    secret_type  TEXT NOT NULL,
    type_version TEXT NOT NULL DEFAULT '',
    fields_schema JSONB NOT NULL,
    -- Optional bilingual display name object, e.g. {"en":"…","zh":"…"}.
    display_name JSONB NOT NULL DEFAULT '{}'::jsonb,
    source       TEXT NOT NULL DEFAULT 'edge'
                 CHECK (source IN ('edge','manual')),
    registered_by UUID REFERENCES device(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One current descriptor per tenant + type + version. Empty version
    -- means unversioned.
    CONSTRAINT edge_secret_type_pk
        UNIQUE (tenant_id, secret_type, type_version)
);

CREATE INDEX IF NOT EXISTS edge_secret_type_tenant_idx
    ON edge_secret_type (tenant_id);

-- Extend the service-account scope whitelist with the ADR-0034 edge-secret
-- permission points, keeping this CHECK in lockstep with
-- rbac.KnownPermissionPoints. The constraint must be dropped and recreated.
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
        'stats:read', 'presence:read',
        'notification:read',
        'quota:read', 'retention:read',
        'serviceaccount:read', 'serviceaccount:write'
    ]::text[]
);
