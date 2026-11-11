-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0053_ota_task_event.up.sql adds the per-device OTA phase timeline and the
-- selectable success policy (ADR-0006c). A rollout previously kept a single
-- ota_task_item row whose state was overwritten, so there was no history of
-- how a device moved through download/verify/apply/reboot or why it failed.
--
-- This migration is additive and forward compatible:
--   * ota_task gains success_policy ('device_reported' by default, preserving
--     the existing immediate-success behaviour) and canceled/skipped counters.
--   * ota_task_item gains result_code (the structured failure/reason axis,
--     orthogonal to the ordered phase) and bytes_received (latest byte count).
--   * the item state check and ota_progress_order() gain the new intermediate
--     phases initiated/verified/updated/rebooting and the canceled terminal.
--   * ota_task_event is the append-only timeline: one row per phase boundary,
--     terminal outcome or cancel. High-frequency download percentage frames
--     are NOT inserted here (they only update the item's latest value); the
--     (task_id, device_id, seq) unique index enforces a per-device ordering.

ALTER TABLE ota_task
    ADD COLUMN IF NOT EXISTS success_policy TEXT NOT NULL DEFAULT 'device_reported'
        CHECK (success_policy IN ('device_reported', 'confirmed_after_reboot')),
    ADD COLUMN IF NOT EXISTS canceled_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS skipped_count INTEGER NOT NULL DEFAULT 0;

ALTER TABLE ota_task_item
    ADD COLUMN IF NOT EXISTS result_code TEXT,
    ADD COLUMN IF NOT EXISTS bytes_received BIGINT NOT NULL DEFAULT 0
        CHECK (bytes_received >= 0);

-- Widen the item phase whitelist. 'confirmed' is a device-reported timeline
-- phase only: a matching confirmation is normalized to item state succeeded,
-- so it is deliberately absent from this check.
ALTER TABLE ota_task_item
    DROP CONSTRAINT IF EXISTS ota_task_item_state_check;
ALTER TABLE ota_task_item
    ADD CONSTRAINT ota_task_item_state_check
    CHECK (state IN ('pending', 'initiated', 'downloading', 'downloaded',
                     'verified', 'updating', 'updated', 'rebooting',
                     'succeeded', 'failed', 'canceled', 'skipped'));

-- Replace the ordinal helper with the extended phase ladder. All terminal
-- states share the top ordinal; a progress UPDATE may never move an item
-- backwards, and terminals are never rewritten by the progress path.
CREATE OR REPLACE FUNCTION ota_progress_order(state TEXT) RETURNS INTEGER AS $$
    SELECT CASE state
        WHEN 'pending'     THEN 0
        WHEN 'initiated'   THEN 1
        WHEN 'downloading' THEN 2
        WHEN 'downloaded'  THEN 3
        WHEN 'verified'    THEN 4
        WHEN 'updating'    THEN 5
        WHEN 'updated'     THEN 6
        WHEN 'rebooting'   THEN 7
        WHEN 'confirmed'   THEN 8
        WHEN 'succeeded'   THEN 9
        WHEN 'failed'      THEN 9
        WHEN 'canceled'    THEN 9
        WHEN 'skipped'     THEN 9
        ELSE 0
    END;
$$ LANGUAGE sql IMMUTABLE;

-- Append-only per-device phase timeline.
CREATE TABLE IF NOT EXISTS ota_task_event (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    task_id UUID NOT NULL REFERENCES ota_task(id) ON DELETE CASCADE,
    device_id UUID NOT NULL,
    -- Gateway-attached sub-devices record the owning gateway; direct devices
    -- leave it NULL.
    gateway_id UUID,
    -- Monotonic per (task, device), starting at 0 for the platform 'queued'
    -- event inserted at task creation.
    seq BIGINT NOT NULL,
    phase TEXT NOT NULL
        CHECK (phase IN ('pending', 'initiated', 'downloading', 'downloaded',
                         'verified', 'updating', 'updated', 'rebooting',
                         'confirmed', 'succeeded', 'failed',
                         'canceled', 'skipped')),
    -- Structured reason axis; NULL/'' for in-progress and successful phases.
    result_code TEXT
        CHECK (result_code IS NULL OR result_code IN (
            'none', 'flash_full', 'out_of_ram', 'connection_lost',
            'integrity_check_failed', 'unsupported_type', 'model_mismatch',
            'version_mismatch', 'invalid_uri', 'apply_failed',
            'reboot_timeout', 'download_no_progress', 'already_current',
            'canceled', 'unknown')),
    progress_pct INTEGER NOT NULL DEFAULT 0
        CHECK (progress_pct BETWEEN 0 AND 100),
    bytes_received BIGINT,
    message TEXT,
    -- Who produced the event: the platform (dispatch/cancel), the gateway
    -- (uplink for a sub-device) or the device itself (direct ingress).
    source TEXT NOT NULL CHECK (source IN ('platform', 'device', 'gateway')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (task_id, device_id, seq)
);

-- Timeline reads are per task, optionally per device, in event order.
CREATE INDEX IF NOT EXISTS ota_task_event_task_idx
    ON ota_task_event (task_id, device_id, seq);

CREATE INDEX IF NOT EXISTS ota_task_event_tenant_idx
    ON ota_task_event (tenant_id, created_at DESC);
