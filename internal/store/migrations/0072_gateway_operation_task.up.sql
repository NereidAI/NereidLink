-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0072_gateway_operation_task.up.sql stores the immediate whole-gateway
-- operation invocations an operator triggers from the gateway Operations
-- tab (ADR-0035). Every row is terminal immediately: nothing is ever
-- queued for an offline gateway (NOT_CONNECTED is the stored result), and
-- the persisted params contain secret:// references only -- plaintext is
-- substituted at the MQTT last mile and never written here.
--
-- Retention: completed rows (all non-PENDING statuses) older than the
-- 90-day retention window are deleted by the opinvoke sweep; the chained
-- audit log keeps the permanent who/when/which-gateway/which-operation
-- record without params or result content.

-- agent_operations records the operation keys the gateway advertised in
-- its last successful hello, so the Operations tab can show only actions
-- this gateway's firmware actually implements (tenant-defined descriptors
-- are always offered). Stamped at hello, read-only over REST.
ALTER TABLE device ADD COLUMN agent_operations JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS gateway_operation_task (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id  UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    operation   TEXT NOT NULL,
    type_version TEXT NOT NULL DEFAULT '',
    -- Params as sent: secret:// references, never plaintext. Validated
    -- against the 64KB ceiling before insert.
    params      JSONB NOT NULL DEFAULT '{}'::jsonb,
    result      JSONB,
    status      TEXT NOT NULL DEFAULT 'PENDING'
                CHECK (status IN (
                    'PENDING','SUCCEEDED','FAILED','TIMEOUT',
                    'NOT_CONNECTED')),
    reason      TEXT NOT NULL DEFAULT '',
    risk        TEXT NOT NULL
                CHECK (risk IN ('read','normal','dangerous')),
    invoked_by       UUID NOT NULL,
    invoked_by_email TEXT NOT NULL DEFAULT '',
    started_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    finished_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- History view: latest tasks first per gateway.
CREATE INDEX IF NOT EXISTS gateway_operation_task_gateway_idx
    ON gateway_operation_task (tenant_id, gateway_id, started_at DESC);

-- Retention sweep selects terminal rows older than the cutoff.
CREATE INDEX IF NOT EXISTS gateway_operation_task_sweep_idx
    ON gateway_operation_task (started_at)
    WHERE status <> 'PENDING';
