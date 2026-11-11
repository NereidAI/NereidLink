-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse the rule-chain grouping: bindings are dropped first, then the
-- group link on version rows and the rule_chain_group table itself.
-- rule_chain rows survive and keep their tenant-root semantics.

ALTER TABLE device DROP COLUMN IF EXISTS rule_chain_group_id;
ALTER TABLE device_profile DROP COLUMN IF EXISTS rule_chain_group_id;

ALTER TABLE rule_chain DROP CONSTRAINT IF EXISTS rule_chain_group_fkey;
ALTER TABLE rule_chain DROP COLUMN IF EXISTS chain_group_id;

DROP TABLE IF EXISTS rule_chain_group;
