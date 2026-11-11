-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0037_service_account.down.sql reverses the machine-identity slice. Child
-- rows drop first to satisfy the foreign key.

DROP TABLE IF EXISTS service_account_secret;
DROP TABLE IF EXISTS service_account;
