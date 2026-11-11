-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0057_device_alias_notes.down.sql removes the operator-facing device text
-- fields. Dropping them discards the alias/notes content; identity
-- (canonical_name) and all time series are unaffected.

ALTER TABLE device
    DROP COLUMN IF EXISTS notes,
    DROP COLUMN IF EXISTS alias;
