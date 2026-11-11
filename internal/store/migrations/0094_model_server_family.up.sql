-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0094_model_server_family.up.sql reserves the per-model concrete model-server
-- product family in the edge manifest models segment (ADR-0039, protocol
-- docs/protocols/nereid-edge/v1.md section 15.2). The existing `backend`
-- column stores the inference protocol family (kserve/openai/task-runtime),
-- which is never a vendor name; `server_family` carries the concrete serving
-- product token reported by the edge (e.g. ovms/triton/external).
--
-- The field is optional on the wire and omitted by every firmware build
-- predating this migration, so the column is NOT NULL with an empty-string
-- default, matching the other optional text inventory columns. Rows keep
-- reporting '' until an edge sends server_family; the console falls back to
-- `backend` for display.

ALTER TABLE gateway_model_inventory
    ADD COLUMN IF NOT EXISTS server_family TEXT NOT NULL DEFAULT '';
