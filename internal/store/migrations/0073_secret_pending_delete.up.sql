-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0073_secret_pending_delete.up.sql records vault secrets whose deletion an
-- operator forced while copies were still delivered to gateways (ADR-0034
-- delete protection). Withdrawal is declarative: each binding moves to
-- REMOVING and the edge reports REMOVED; an offline gateway cannot confirm
-- until it reconnects, so the vault original must NOT be hard-deleted until
-- every binding is terminal. A row here therefore means "deleting: waiting
-- for the last gateways to confirm removal".
--
-- There is deliberately no FK to tenant_secret: bindings and this table
-- reference the vault by name only, and the target row is deleted
-- last. The pending row is removed atomically with the vault delete by the
-- secretdelete reconciler/sweep.

CREATE TABLE IF NOT EXISTS tenant_secret_pending_delete (
    tenant_id    UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    secret_name  TEXT NOT NULL,
    -- Human operator who forced the deletion (step-up verified); a machine
    -- principal may never create one of these rows.
    initiated_by UUID,
    initiated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, secret_name)
);

-- The sweep lists pending rows in insertion order.
CREATE INDEX IF NOT EXISTS tenant_secret_pending_delete_time_idx
    ON tenant_secret_pending_delete (initiated_at);
