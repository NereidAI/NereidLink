-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Canvas coordinates for the React Flow rule-chain editor. Positions are
-- presentation-only metadata: rule execution ignores them. Nullable so
-- chains authored before the editor fall back to automatic layout.

ALTER TABLE rule_node ADD COLUMN IF NOT EXISTS position_x REAL;
ALTER TABLE rule_node ADD COLUMN IF NOT EXISTS position_y REAL;
