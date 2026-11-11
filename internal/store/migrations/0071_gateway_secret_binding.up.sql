-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0071_gateway_secret_binding.up.sql stores the desired-state bindings that
-- deliver tenant vault secrets to one gateway (ADR-0034). Each row binds one
-- secret name to one gateway, carries the generic target selectors
-- ([{kind,value}], first version uses kind='mac'), the monotonically
-- increasing revision and the delivery status.
--
-- Lifecycle: PENDING -> DISPATCHED -> APPLIED (or FAILED + reason);
-- withdrawal: REMOVING until the edge reports removal, then REMOVED.
-- Offline edits only change the desired row; the latest revision is what
-- gets delivered. Rows cascade with tenant/gateway.

-- agent_features records the feature names negotiated in the gateway's last
-- successful hello ("secrets" marks an edge that accepts secret delivery).
-- It is stamped at hello and read-only over REST, so the console can show
-- unsupported firmware instead of pretending a push is in flight.
ALTER TABLE device ADD COLUMN agent_features JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE IF NOT EXISTS gateway_secret_binding (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id  UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    secret_name TEXT NOT NULL,
    secret_type TEXT NOT NULL DEFAULT '',
    type_version TEXT NOT NULL DEFAULT '',
    selectors   JSONB NOT NULL DEFAULT '[]'::jsonb,
    revision    BIGINT NOT NULL DEFAULT 1,
    status      TEXT NOT NULL DEFAULT 'PENDING'
                CHECK (status IN (
                    'PENDING','DISPATCHED','APPLIED','FAILED',
                    'REMOVING','REMOVED')),
    reason      TEXT NOT NULL DEFAULT '',
    -- Sweep claim flags make alarm raising at-most-once across restarts and
    -- replicas (mirrors connector_instance): not_applied_alarm is re-armed
    -- by each desired edit, failed_alarm by a FAILED report, failed_critical
    -- by the MAJOR->CRITICAL escalation sweep. desired_at anchors the
    -- pending timeout; failed_at anchors escalation.
    not_applied_alarm BOOLEAN NOT NULL DEFAULT FALSE,
    failed_alarm      BOOLEAN NOT NULL DEFAULT FALSE,
    failed_critical   BOOLEAN NOT NULL DEFAULT FALSE,
    desired_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    failed_at   TIMESTAMPTZ,
    dispatched_at TIMESTAMPTZ,
    applied_at    TIMESTAMPTZ,
    removed_at    TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT gateway_secret_binding_pk UNIQUE (gateway_id, secret_name)
);

CREATE INDEX IF NOT EXISTS gateway_secret_binding_gateway_idx
    ON gateway_secret_binding (gateway_id);

CREATE INDEX IF NOT EXISTS gateway_secret_binding_secret_idx
    ON gateway_secret_binding (tenant_id, secret_name);

CREATE INDEX IF NOT EXISTS gateway_secret_binding_status_idx
    ON gateway_secret_binding (status);
