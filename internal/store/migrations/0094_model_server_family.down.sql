-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0094_model_server_family.down.sql removes the reserved model-server
-- product-family column. No table data other than that column is affected.

ALTER TABLE gateway_model_inventory
    DROP COLUMN IF EXISTS server_family;
