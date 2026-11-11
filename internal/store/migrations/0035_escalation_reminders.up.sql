-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0035_escalation_reminders.up.sql adds ADR-0019 repeated reminders. An
-- enabled chain may carry a repeat interval: while a bound alarm stays
-- unacknowledged (state ACTIVE), the runner re-notifies a chosen role at
-- the fixed cadence. Reminders stop the moment the alarm is acknowledged
-- or cleared (no state row is materialized once it leaves ACTIVE).
--
-- repeat_interval_seconds = 0 (default) means reminders are off, so
-- existing chains behave exactly as before.

ALTER TABLE escalation_chain
    ADD COLUMN IF NOT EXISTS repeat_interval_seconds BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS repeat_target_role TEXT;

ALTER TABLE escalation_chain
    ADD CONSTRAINT escalation_chain_repeat_interval_chk
    CHECK (repeat_interval_seconds >= 0);

-- A repeat role is mandatory exactly when a positive interval is set;
-- it must be one of the tenant roles escalation may target.
ALTER TABLE escalation_chain
    ADD CONSTRAINT escalation_chain_repeat_role_chk
    CHECK (
        repeat_interval_seconds = 0
        OR repeat_target_role IN
           ('TENANT_ADMIN','ENGINEER','CUSTOMER_USER','READ_ONLY')
    );

-- Per-alarm reminder cursor: one row per alarm that still gets reminders.
CREATE TABLE alarm_reminder_state (
    alarm_id UUID PRIMARY KEY REFERENCES alarm(id) ON DELETE CASCADE,
    next_fire_at TIMESTAMPTZ NOT NULL,
    fired_count INTEGER NOT NULL DEFAULT 0
);

-- The scheduler scans due reminder cursors.
CREATE INDEX alarm_reminder_due_idx
    ON alarm_reminder_state (next_fire_at);
