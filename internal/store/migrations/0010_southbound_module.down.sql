-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

ALTER TABLE device_credential DROP COLUMN IF EXISTS extra;
ALTER TABLE device DROP COLUMN IF EXISTS protocol;
DROP TABLE IF EXISTS southbound_module;
