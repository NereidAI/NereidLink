-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0048_ota_task.down.sql removes firmware OTA task state. OTA progress is
-- operational data; dropping the rows loses no device, telemetry or desired
-- configuration state.

DROP TABLE IF EXISTS ota_task_item;
DROP TABLE IF EXISTS ota_task;
