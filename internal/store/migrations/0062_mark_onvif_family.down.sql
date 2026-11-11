-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0062_mark_onvif_family.down.sql reverses the family backfill as far as
-- possible. The up migration stores no per-row provenance, so a row that was
-- explicitly tagged protocol='onvif' by an operator/import before this
-- migration is indistinguishable from one the backfill set; this down
-- therefore blanks the family on every same-name edge/import row currently
-- tagged 'onvif'. It is a best-effort rollback and is documented as such.
-- Re-applying the migration or re-selecting the family in the console restores
-- the intended value.

UPDATE device_profile
SET protocol = '',
    updated_at = now()
WHERE source IN ('edge', 'import')
  AND protocol = 'onvif'
  AND lower(name) ~ '\yonvif\y';
