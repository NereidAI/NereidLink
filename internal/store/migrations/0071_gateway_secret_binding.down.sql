-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0071_gateway_secret_binding.down.sql removes the delivery binding table.
-- Gateway-side secrets become undeliverable; the vault source of truth is
-- unaffected.

DROP TABLE IF EXISTS gateway_secret_binding;

ALTER TABLE device DROP COLUMN IF EXISTS agent_features;
