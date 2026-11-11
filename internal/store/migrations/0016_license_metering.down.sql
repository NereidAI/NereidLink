-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

DROP TABLE IF EXISTS tenant_usage_daily;
DROP TABLE IF EXISTS license_clock_witness;
DROP TABLE IF EXISTS tenant_license;
ALTER TABLE tenant DROP COLUMN IF EXISTS updated_at;
