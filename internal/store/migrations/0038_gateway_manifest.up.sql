-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0038_gateway_manifest.up.sql records the outcome of the 5-minute gateway
-- manifest reconciliation (ADR-0026). Each row is one reconciled cycle for
-- one gateway: the desired/reported classification and the presence actions
-- taken. Rows are append-only history for observability and audit; they are
-- never updated.
--
-- The reported device ids are stored as text[] to keep one row per cycle
-- instead of a child table that would grow three rows per cycle per gateway.
-- The gateway_id foreign key is ON DELETE CASCADE so decommissioning a
-- gateway device removes its reconciliation history with the device.

CREATE TABLE gateway_manifest (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id       UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    observed_at      TIMESTAMPTZ NOT NULL,
    revision         BIGINT NOT NULL DEFAULT 0,
    missed_cycles    INTEGER NOT NULL DEFAULT 0,
    in_sync          TEXT[] NOT NULL DEFAULT '{}',
    missing          TEXT[] NOT NULL DEFAULT '{}',
    unregistered     TEXT[] NOT NULL DEFAULT '{}',
    online_promoted  INTEGER NOT NULL DEFAULT 0,
    offline_derived  INTEGER NOT NULL DEFAULT 0,
    drift            BOOLEAN NOT NULL DEFAULT FALSE
);

-- Operators and dashboards query a gateway's reconciliation history newest
-- first; the tenant index supports tenant-scoped fleet views.
CREATE INDEX gateway_manifest_gateway_time_idx
    ON gateway_manifest (gateway_id, observed_at DESC);

CREATE INDEX gateway_manifest_tenant_time_idx
    ON gateway_manifest (tenant_id, observed_at DESC);
