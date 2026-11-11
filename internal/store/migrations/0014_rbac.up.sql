-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0014 adds the RBAC binding model from ADR-0022 on top of the local
-- identity tables: one membership per user per tenant carrying the
-- widest data scope, and one or more fixed built-in roles whose
-- permission points union. Custom roles are P2; no role table yet.

CREATE TABLE IF NOT EXISTS tenant_membership (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    tenant_id UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    -- Data scope of this membership; scope_id anchors CUSTOMER/GROUP.
    scope TEXT NOT NULL DEFAULT 'TENANT'
        CHECK (scope IN ('TENANT', 'CUSTOMER', 'GROUP', 'SELF')),
    scope_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, tenant_id)
);

CREATE INDEX IF NOT EXISTS tenant_membership_tenant_idx
    ON tenant_membership (tenant_id);

CREATE TABLE IF NOT EXISTS membership_role (
    id UUID PRIMARY KEY,
    membership_id UUID NOT NULL
        REFERENCES tenant_membership(id) ON DELETE CASCADE,
    -- Built-in role key; custom cloned roles arrive with P2.
    role TEXT NOT NULL
        CHECK (role IN (
            'PLATFORM_ADMIN', 'TENANT_ADMIN', 'ENGINEER',
            'CUSTOMER_USER', 'READ_ONLY', 'SERVICE_ACCOUNT')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (membership_id, role)
);
