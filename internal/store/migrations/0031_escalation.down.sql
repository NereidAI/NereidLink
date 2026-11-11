-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse of 0031: drop escalation tables and the alarm link column.
DROP TABLE IF EXISTS alarm_escalation_state;
DROP TABLE IF EXISTS escalation_step;
DROP TABLE IF EXISTS escalation_chain;

ALTER TABLE alarm
    DROP COLUMN IF EXISTS escalation_chain_id;
