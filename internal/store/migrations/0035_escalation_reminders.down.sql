-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0035_escalation_reminders.down.sql removes ADR-0019 repeated reminders.

DROP INDEX IF EXISTS alarm_reminder_due_idx;
DROP TABLE IF EXISTS alarm_reminder_state;

ALTER TABLE escalation_chain DROP CONSTRAINT IF EXISTS escalation_chain_repeat_role_chk;
ALTER TABLE escalation_chain DROP CONSTRAINT IF EXISTS escalation_chain_repeat_interval_chk;
ALTER TABLE escalation_chain
    DROP COLUMN IF EXISTS repeat_target_role,
    DROP COLUMN IF EXISTS repeat_interval_seconds;
