-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0053_ota_task_event.down.sql reverses the OTA phase timeline and success
-- policy. Data impact: the append-only event history is dropped, and items
-- lose their result_code/bytes columns; the current single-row item state and
-- the task aggregate are preserved.

DROP TABLE IF EXISTS ota_task_event;

-- Restore the pre-0053 ordinal ladder.
CREATE OR REPLACE FUNCTION ota_progress_order(state TEXT) RETURNS INTEGER AS $$
    SELECT CASE state
        WHEN 'pending' THEN 0
        WHEN 'downloading' THEN 1
        WHEN 'downloaded' THEN 2
        WHEN 'updating' THEN 3
        WHEN 'succeeded' THEN 4
        WHEN 'failed' THEN 4
        ELSE 0
    END;
$$ LANGUAGE sql IMMUTABLE;

-- Collapse any rows that only exist in the extended model back to the closest
-- pre-0053 state so the original check can be re-imposed.
UPDATE ota_task_item SET state='pending'     WHERE state='initiated';
UPDATE ota_task_item SET state='downloaded'  WHERE state='verified';
UPDATE ota_task_item SET state='updating'    WHERE state IN ('updated','rebooting');
-- 'canceled' items were only produced by the new cancel operation; mark them
-- skipped so they remain distinguishable from failures under the old check.
UPDATE ota_task_item SET state='skipped'     WHERE state='canceled';

ALTER TABLE ota_task_item
    DROP CONSTRAINT IF EXISTS ota_task_item_state_check;
ALTER TABLE ota_task_item
    ADD CONSTRAINT ota_task_item_state_check
    CHECK (state IN ('pending', 'downloading', 'downloaded', 'updating',
                     'succeeded', 'failed', 'skipped'));

ALTER TABLE ota_task_item
    DROP COLUMN IF EXISTS result_code,
    DROP COLUMN IF EXISTS bytes_received;

ALTER TABLE ota_task
    DROP COLUMN IF EXISTS success_policy,
    DROP COLUMN IF EXISTS canceled_count,
    DROP COLUMN IF EXISTS skipped_count;
