-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse migration for asynchronous batch command tasks. Individual
-- command rows are retained because they are independent audit records.

DROP TABLE IF EXISTS command_task_item;
DROP TABLE IF EXISTS command_task;
