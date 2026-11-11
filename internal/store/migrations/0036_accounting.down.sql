-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0036_accounting.down.sql removes the ADR-0025 accounting store.
--
-- The retention policy must be removed before the hypertable: dropping a
-- hypertable that still carries a policy leaves the background worker
-- holding a stale reference and logs an error on every cycle.

SELECT remove_retention_policy('accounting_topn', if_exists => TRUE);

DROP TABLE IF EXISTS accounting_topn;