-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse of 0008: drop the rule-node canvas coordinates.

ALTER TABLE rule_node DROP COLUMN IF EXISTS position_y;
ALTER TABLE rule_node DROP COLUMN IF EXISTS position_x;
