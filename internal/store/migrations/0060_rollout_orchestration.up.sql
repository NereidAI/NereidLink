-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0060_rollout_orchestration.up.sql adds dynamic target selectors,
-- batched/canary rollout, abort thresholds and platform watchdogs on top of
-- the ADR-0006b/c single-batch model (ADR-0006d).
--
-- Forward compatibility: every new policy column is nullable. A task with
-- NULL policy columns (all pre-0060 rows and any request that omits them)
-- behaves exactly as before — one batch dispatched immediately, no abort
-- threshold, no watchdog — so legacy external-URL tasks keep their semantics.

-- Dynamic target snapshot provenance: 'explicit' (the default) or 'selector'.
ALTER TABLE ota_task
    ADD COLUMN IF NOT EXISTS target_mode TEXT NOT NULL DEFAULT 'explicit'
        CHECK (target_mode IN ('explicit', 'selector')),
    ADD COLUMN IF NOT EXISTS target_selector JSONB NOT NULL DEFAULT '{}'::jsonb;

-- Batching: exactly one of batch_percent/batch_size is set for a batched
-- rollout; both NULL means one full batch (legacy behaviour).
ALTER TABLE ota_task
    ADD COLUMN IF NOT EXISTS batch_percent INTEGER
        CHECK (batch_percent IS NULL OR batch_percent BETWEEN 1 AND 100),
    ADD COLUMN IF NOT EXISTS batch_size INTEGER
        CHECK (batch_size IS NULL OR batch_size > 0),
    ADD COLUMN IF NOT EXISTS batch_pause_seconds INTEGER NOT NULL DEFAULT 0
        CHECK (batch_pause_seconds >= 0),
    ADD COLUMN IF NOT EXISTS batch_no INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS next_batch_at TIMESTAMPTZ;

-- Auto-abort thresholds; NULL disables that threshold.
ALTER TABLE ota_task
    ADD COLUMN IF NOT EXISTS failure_abort_percent INTEGER
        CHECK (failure_abort_percent IS NULL
               OR failure_abort_percent BETWEEN 1 AND 100),
    ADD COLUMN IF NOT EXISTS max_consecutive_failures INTEGER
        CHECK (max_consecutive_failures IS NULL OR max_consecutive_failures > 0);

-- Platform watchdogs; NULL leaves the item open indefinitely (legacy).
ALTER TABLE ota_task
    ADD COLUMN IF NOT EXISTS stall_timeout_seconds INTEGER
        CHECK (stall_timeout_seconds IS NULL OR stall_timeout_seconds > 0),
    ADD COLUMN IF NOT EXISTS confirm_timeout_seconds INTEGER
        CHECK (confirm_timeout_seconds IS NULL OR confirm_timeout_seconds > 0),
    ADD COLUMN IF NOT EXISTS abort_reason TEXT;

-- Aggregate state gains 'paused' (operator hold, no new batches) and
-- 'aborted' (auto-abort by a threshold; distinct from human 'canceled').
ALTER TABLE ota_task
    DROP CONSTRAINT IF EXISTS ota_task_state_check;
ALTER TABLE ota_task
    ADD CONSTRAINT ota_task_state_check
    CHECK (state IN ('pending', 'running', 'paused', 'completed', 'partial',
                     'failed', 'aborted', 'canceled'));

-- Batched items remember which batch opened them (0 = single legacy batch).
ALTER TABLE ota_task_item
    ADD COLUMN IF NOT EXISTS batch_no INTEGER NOT NULL DEFAULT 0;

-- The orchestrator repeatedly looks up running tasks with due batches and
-- open items to watchdog.
CREATE INDEX IF NOT EXISTS ota_task_open_state_idx
    ON ota_task (state)
    WHERE state IN ('pending', 'running', 'paused');

CREATE INDEX IF NOT EXISTS ota_task_item_open_batch_idx
    ON ota_task_item (task_id, batch_no, state)
    WHERE state IN ('pending', 'initiated', 'downloading', 'downloaded',
                    'verified', 'updating', 'updated', 'rebooting');
