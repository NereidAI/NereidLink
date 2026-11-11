-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse 0015: drop audit tables. Audit history and seals are
-- removed; operator-initiated rollback, not routine operation.

DROP TABLE IF EXISTS audit_daily_seal;
DROP TABLE IF EXISTS audit_event;
