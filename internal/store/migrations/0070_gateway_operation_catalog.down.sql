-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0070_gateway_operation_catalog.down.sql removes the gateway operation
-- catalog tables. Operation invocation (ticket 06) must be rolled back
-- together with this migration.

DROP TABLE IF EXISTS tenant_operation_disabled;
DROP TABLE IF EXISTS tenant_operation;
DROP TABLE IF EXISTS gateway_operation;
