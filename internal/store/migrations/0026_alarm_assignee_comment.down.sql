-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Remove alarm comments and assignment. Comment history is operational ticket
-- state, so it is deleted with its table; alarm rows otherwise remain.

DROP TABLE IF EXISTS alarm_comment;

ALTER TABLE alarm
    DROP COLUMN IF EXISTS assignee_id;
