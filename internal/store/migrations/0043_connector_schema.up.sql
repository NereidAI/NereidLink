-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0043_connector_schema.up.sql stores the JSON Schemas an edge registers for
-- the connector types it supports (ADR-0026 deferred slice: edge-registered
-- config schema validation). A negotiated gateway advertises an inline
-- `config_schema` per (connector_type, connector_type_version) in hello; NL
-- upserts it here and later validates profile template config and connector
-- instance overrides against the matching schema before persisting a desired
-- spec. The kernel never interprets connector protocols; the schema itself is
-- opaque JSONB validated with a draft 2020-12 engine at write time.

CREATE TABLE IF NOT EXISTS connector_schema (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    connector_type       TEXT NOT NULL,
    connector_type_version TEXT NOT NULL DEFAULT '',
    config_schema JSONB NOT NULL,
    source       TEXT NOT NULL DEFAULT 'edge',
    registered_by UUID REFERENCES device(id) ON DELETE SET NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One current schema per tenant + type + version. Re-registration from a
    -- reconnecting edge upserts (replaces) the document rather than
    -- duplicating it; empty version means "any/unversioned".
    UNIQUE (tenant_id, connector_type, connector_type_version)
);

CREATE INDEX IF NOT EXISTS connector_schema_tenant_idx
    ON connector_schema (tenant_id);
