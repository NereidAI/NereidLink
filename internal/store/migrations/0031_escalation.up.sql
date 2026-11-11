-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0031 introduces alarm escalation chains (ADR-0019). A chain is an
-- ordered set of steps; each step waits a delay measured from the alarm
-- start, then escalates to the holders of a tenant role. Steps stop
-- automatically when the alarm is acknowledged or cleared. The
-- per-alarm firing state makes each step fire at most once and lets a
-- background scheduler claim due rows safely.

CREATE TABLE escalation_chain (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    name TEXT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT true,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX escalation_chain_tenant_name_idx
    ON escalation_chain (tenant_id, name);

CREATE TABLE escalation_step (
    id UUID PRIMARY KEY,
    chain_id UUID NOT NULL REFERENCES escalation_chain(id) ON DELETE CASCADE,
    step_order INTEGER NOT NULL,
    delay_seconds BIGINT NOT NULL,
    target_role TEXT NOT NULL,
    stop_on_ack BOOLEAN NOT NULL DEFAULT true,
    stop_on_clear BOOLEAN NOT NULL DEFAULT true,
    CONSTRAINT escalation_step_delay_chk CHECK (delay_seconds >= 0),
    CONSTRAINT escalation_step_order_chk CHECK (step_order >= 0),
    CONSTRAINT escalation_step_unique UNIQUE (chain_id, step_order)
);

-- Bind a chain to an alarm when the alarm is raised.
ALTER TABLE alarm
    ADD COLUMN escalation_chain_id UUID REFERENCES escalation_chain(id) ON DELETE SET NULL;

-- Per-alarm, per-step firing bookkeeping.
CREATE TABLE alarm_escalation_state (
    alarm_id UUID NOT NULL REFERENCES alarm(id) ON DELETE CASCADE,
    step_id UUID NOT NULL REFERENCES escalation_step(id) ON DELETE CASCADE,
    state TEXT NOT NULL CHECK (state IN ('PENDING','FIRED','CANCELED')),
    fire_at TIMESTAMPTZ NOT NULL,
    fired_at TIMESTAMPTZ,
    PRIMARY KEY (alarm_id, step_id)
);

-- The scheduler scans due pending rows.
CREATE INDEX alarm_escalation_due_idx
    ON alarm_escalation_state (state, fire_at);
