-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0059_stable_exclusivity.down.sql removes the exclusivity backstop;
-- multiple starred packages per scope become possible again.
DROP INDEX IF EXISTS firmware_pkg_one_stable_per_scope_uniq;
