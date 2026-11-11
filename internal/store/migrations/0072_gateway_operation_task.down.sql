-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0072_gateway_operation_task.down.sql removes the gateway operation task
-- history and the hello-stamped advertised operation set.
--
-- Data note: operation results are 90-day operational records and the
-- permanent evidence lives in the chained audit log, so dropping the table
-- loses no required audit trail.

DROP TABLE IF EXISTS gateway_operation_task;

ALTER TABLE device DROP COLUMN IF EXISTS agent_operations;
