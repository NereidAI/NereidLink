-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0018 introduces the ownership and lifecycle model from ADR-0013:
-- optional Customer ownership, a tree of Device Groups, time-limited
-- single-use claiming tokens and an append-only ownership history.
-- Devices gain a lifecycle state; rows are never physically deleted,
-- they move through UNCLAIMED -> ACTIVE -> DECOMMISSIONED (and a
-- reserved ARCHIVED state used by the later cold-archive stage).

-- Customers are an optional business grouping inside a tenant.
CREATE TABLE IF NOT EXISTS customer (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    name TEXT NOT NULL,
    contact_name TEXT NOT NULL DEFAULT '',
    contact_email TEXT NOT NULL DEFAULT '',
    external_id TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS customer_tenant_idx
    ON customer (tenant_id);

-- Device groups form a parent-linked tree. A group may optionally be
-- anchored to one customer; the service layer validates the subtree and
-- rejects cycles.
CREATE TABLE IF NOT EXISTS device_group (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    customer_id UUID REFERENCES customer(id),
    parent_id UUID REFERENCES device_group(id),
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS device_group_tenant_idx
    ON device_group (tenant_id);

CREATE INDEX IF NOT EXISTS device_group_parent_idx
    ON device_group (parent_id);

-- Claiming tokens. Only the SHA-256 hash is stored; the plaintext is
-- returned once at issuance. A token is single use and time bounded.
CREATE TABLE IF NOT EXISTS device_claim (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL REFERENCES device(id),
    token_hash TEXT NOT NULL UNIQUE,
    status TEXT NOT NULL DEFAULT 'ISSUED'
        CHECK (status IN ('ISSUED', 'CLAIMED', 'EXPIRED', 'REVOKED')),
    expires_at TIMESTAMPTZ NOT NULL,
    claimed_by UUID,
    claimed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS device_claim_device_idx
    ON device_claim (device_id);

-- Append-only ownership history: registration, claim, transfer and the
-- decommission/reactivate boundaries. Rows are never updated or deleted.
CREATE TABLE IF NOT EXISTS device_ownership_event (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL REFERENCES device(id),
    event_type TEXT NOT NULL CHECK (event_type IN (
        'registered', 'claimed', 'transferred',
        'decommissioned', 'reactivated')),
    from_customer_id UUID,
    to_customer_id UUID,
    from_group_id UUID,
    to_group_id UUID,
    actor_id UUID,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS device_ownership_event_device_idx
    ON device_ownership_event (device_id, created_at DESC);

-- Lifecycle columns on the device row. Existing devices predate the
-- model and are already in normal use, so they backfill as ACTIVE.
ALTER TABLE device
    ADD COLUMN IF NOT EXISTS customer_id UUID REFERENCES customer(id),
    ADD COLUMN IF NOT EXISTS group_id UUID REFERENCES device_group(id),
    ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (state IN ('UNCLAIMED', 'ACTIVE', 'DECOMMISSIONED', 'ARCHIVED')),
    ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS decommissioned_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS recall_until TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

UPDATE device SET state = 'ACTIVE' WHERE state = 'ACTIVE';

CREATE INDEX IF NOT EXISTS device_tenant_state_idx
    ON device (tenant_id, state);

CREATE INDEX IF NOT EXISTS device_customer_idx
    ON device (customer_id);

CREATE INDEX IF NOT EXISTS device_group_ref_idx
    ON device (group_id);
