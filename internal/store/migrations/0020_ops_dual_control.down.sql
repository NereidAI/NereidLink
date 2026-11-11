-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse of 0020: drop the operation order table and its indexes.

DROP INDEX IF EXISTS ops_order_initiator_idx;
DROP INDEX IF EXISTS ops_order_tenant_idx;
DROP INDEX IF EXISTS ops_order_status_created_idx;

DROP TABLE IF EXISTS ops_order;
