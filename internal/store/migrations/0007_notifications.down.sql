-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse of 0007: drop the in-app notification table.

DROP INDEX IF EXISTS notification_tenant_time_idx;
DROP TABLE IF EXISTS notification;
