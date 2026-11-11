-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0038_gateway_manifest.down.sql removes the manifest reconciliation
-- history (ADR-0026). The KV lease bucket and presence state are runtime
-- data and are not touched by the schema rollback.

DROP INDEX IF EXISTS gateway_manifest_tenant_time_idx;
DROP INDEX IF EXISTS gateway_manifest_gateway_time_idx;
DROP TABLE IF EXISTS gateway_manifest;
