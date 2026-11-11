-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0045_discovery_policy.down.sql removes per-tenant/per-gateway
-- auto-provisioning overrides. Policy then falls back entirely to the
-- deployment YAML auto_provision.* block; no device or discovery data is
-- affected (this table holds configuration only).

DROP TABLE IF EXISTS discovery_policy;
