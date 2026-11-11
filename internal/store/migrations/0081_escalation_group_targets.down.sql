-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

ALTER TABLE escalation_chain
    DROP COLUMN IF EXISTS repeat_target_group_id;

ALTER TABLE escalation_step
    DROP COLUMN IF EXISTS target_group_id;