-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back 0023. Dropping the 'archived' event type does not remove any
-- existing rows: the sweeper only writes that type after this migration is
-- applied, and an operator rolling back has already accepted that history is
-- preserved. The archived device rows themselves are left untouched --
-- reverting must never move devices out of ARCHIVED, because the reaper
-- stopped running with the rollback and no path would bring them back.

DROP INDEX IF EXISTS device_recall_expiry_idx;

ALTER TABLE device_ownership_event
    DROP CONSTRAINT IF EXISTS device_ownership_event_event_type_check;

ALTER TABLE device_ownership_event
    ADD CONSTRAINT device_ownership_event_event_type_check
    CHECK (event_type IN (
        'registered', 'claimed', 'transferred',
        'decommissioned', 'reactivated'));