-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0063_protocol_catalog.down.sql removes the global, operator-editable
-- protocol/device-service catalog introduced in 0063 (ADR-0033). Dropping the
-- table also removes any operator-added custom families; no other table
-- references it, so the drop is self-contained.

DROP INDEX IF EXISTS protocol_catalog_service_idx;
DROP TABLE IF EXISTS protocol_catalog;
