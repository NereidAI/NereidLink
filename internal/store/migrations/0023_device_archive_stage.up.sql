-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0023 closes the decommission lifecycle.
--
-- Before this migration the recall window was decorative: a device whose
-- recall_until had passed stayed DECOMMISSIONED forever. ReactivateDevice
-- refused it (recall window has expired), DecommissionDevice refused it (not
-- ACTIVE or UNCLAIMED), and nothing ever wrote ARCHIVED -- the state existed
-- in the CHECK constraint and in the UI but had no writer anywhere in the
-- codebase. Devices accumulated in a terminal state with no exit.
--
-- This migration adds the 'archived' ownership event type so the sweeper can
-- record the automatic transition. No column changes are needed: archived_at
-- already exists (added in 0018) and the device state CHECK already permits
-- ARCHIVED.

-- Drop and recreate the CHECK with the new event type. The constraint is
-- expanded rather than replaced so concurrent readers are not blocked for the
-- duration of a table rewrite; device_ownership_event is append-only and
-- grows with device count, so a rewrite would be expensive on a large
-- install.
ALTER TABLE device_ownership_event
    DROP CONSTRAINT IF EXISTS device_ownership_event_event_type_check;

ALTER TABLE device_ownership_event
    ADD CONSTRAINT device_ownership_event_event_type_check
    CHECK (event_type IN (
        'registered', 'claimed', 'transferred',
        'decommissioned', 'reactivated', 'archived'));

-- The sweeper looks devices up by expired recall window, so give it a
-- partial index: only DECOMMISSIONED rows can ever match, and archived rows
-- are excluded permanently.
CREATE INDEX IF NOT EXISTS device_recall_expiry_idx
    ON device (recall_until)
    WHERE state = 'DECOMMISSIONED' AND recall_until IS NOT NULL;