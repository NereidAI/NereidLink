-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0060_rollout_orchestration.down.sql removes the rollout-orchestration
-- columns and restores the pre-0060 aggregate state whitelist.

DROP INDEX IF EXISTS ota_task_item_open_batch_idx;
DROP INDEX IF EXISTS ota_task_open_state_idx;

ALTER TABLE ota_task_item
    DROP COLUMN IF EXISTS batch_no;

ALTER TABLE ota_task
    DROP CONSTRAINT IF EXISTS ota_task_state_check;
ALTER TABLE ota_task
    ADD CONSTRAINT ota_task_state_check
    CHECK (state IN ('pending', 'running', 'completed', 'partial',
                     'failed', 'canceled'));

ALTER TABLE ota_task
    DROP COLUMN IF EXISTS abort_reason,
    DROP COLUMN IF EXISTS confirm_timeout_seconds,
    DROP COLUMN IF EXISTS stall_timeout_seconds,
    DROP COLUMN IF EXISTS max_consecutive_failures,
    DROP COLUMN IF EXISTS failure_abort_percent,
    DROP COLUMN IF EXISTS next_batch_at,
    DROP COLUMN IF EXISTS batch_no,
    DROP COLUMN IF EXISTS batch_pause_seconds,
    DROP COLUMN IF EXISTS batch_size,
    DROP COLUMN IF EXISTS batch_percent,
    DROP COLUMN IF EXISTS target_selector,
    DROP COLUMN IF EXISTS target_mode;
