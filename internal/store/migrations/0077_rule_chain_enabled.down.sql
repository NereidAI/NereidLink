-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0077_rule_chain_enabled.down.sql removes the chain-group enabled flag.

ALTER TABLE rule_chain_group DROP COLUMN IF EXISTS enabled;