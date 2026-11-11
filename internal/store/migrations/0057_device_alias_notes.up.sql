-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0057_device_alias_notes.up.sql adds operator-facing descriptive fields to
-- the device row. Both are free-form text and carry no platform semantics:
--   alias — a human-friendly display name distinct from the unique
--           canonical_name used as the device identity/credential subject.
--   notes — free-form additional information (location, contact, remarks).
-- They default to NULL (absent) rather than '' so the API omitempty contract
-- and the UI can distinguish "never set" from an explicitly empty value.

ALTER TABLE device
    ADD COLUMN IF NOT EXISTS alias TEXT,
    ADD COLUMN IF NOT EXISTS notes TEXT;
