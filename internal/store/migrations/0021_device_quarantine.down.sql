-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Roll back the quarantine state. Remaining quarantined devices are
-- restored to ACTIVE before the old four-state constraint is re-added.

ALTER TABLE device DROP CONSTRAINT device_state_check;

UPDATE device
   SET state = 'ACTIVE',
       quarantined_at = NULL,
       quarantine_reason = ''
 WHERE state = 'QUARANTINED';

ALTER TABLE device ADD CONSTRAINT device_state_check CHECK (
    state IN ('UNCLAIMED', 'ACTIVE', 'DECOMMISSIONED', 'ARCHIVED'));

ALTER TABLE device DROP COLUMN IF EXISTS quarantine_reason;
ALTER TABLE device DROP COLUMN IF EXISTS quarantined_at;
