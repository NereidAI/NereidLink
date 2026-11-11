-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0033_retention_policy.down.sql drops the retention override table.
-- Code defaults remain; removing the rows simply restores defaults.

DROP TABLE IF EXISTS retention_policy;
