-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0030 stores user-defined dashboards (ADR-0020). A dashboard is a
-- named, tenant-scoped layout descriptor. The grid layout — widget
-- type, position/size and per-widget binding config — is carried as a
-- JSON array so new widget types never need a schema change.

CREATE TABLE dashboard (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    name TEXT NOT NULL,
    sort_order INTEGER NOT NULL DEFAULT 0,
    layout JSONB NOT NULL DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Dashboard names are unique within a tenant for the picker; list reads
-- are ordered for a stable tab order.
CREATE UNIQUE INDEX dashboard_tenant_name_idx
    ON dashboard (tenant_id, name);
CREATE INDEX dashboard_tenant_order_idx
    ON dashboard (tenant_id, sort_order, name);
