-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0059_stable_exclusivity.up.sql enforces at most one stable package per
-- (tenant, model) scope (ADR-0006d §2). The service clears the previous
-- star in the same transaction; this partial unique index is the database
-- backstop. COALESCE puts profile-agnostic packages (NULL profile_id) in
-- their own scope keyed by the Nil UUID.
--
-- Data preservation: if several rows are already starred in one scope the
-- newest one (latest updated_at, then created_at) keeps the star so the
-- index can be built without failing.

UPDATE firmware_package fp
SET is_stable = false
WHERE fp.is_stable = true
  AND fp.id NOT IN (
      SELECT DISTINCT ON (tenant_id, COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'))
             id
      FROM firmware_package
      WHERE is_stable = true
      ORDER BY tenant_id,
               COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'),
               updated_at DESC, created_at DESC
  );

CREATE UNIQUE INDEX IF NOT EXISTS firmware_pkg_one_stable_per_scope_uniq
    ON firmware_package (tenant_id, COALESCE(profile_id, '00000000-0000-0000-0000-000000000000'))
    WHERE is_stable = true;
