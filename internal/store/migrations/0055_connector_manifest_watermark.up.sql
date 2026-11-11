-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0055_connector_manifest_watermark.up.sql adds a per-gateway watermark for
-- ADR-0026 connector manifest reconciliation. Migration 0044 only added
-- per-instance watermark columns (manifest_revision / reported_config_hash /
-- last_manifest_at); those cannot answer two questions the console needs:
--   1. when did the edge last send its FULL connector list (a spec with a
--      NULL last_manifest_at is indistinguishable "never seen" from "created
--      after the most recent full list");
--   2. the exact set of spec ids the edge reported in its most recent full
--      list. Desired-but-absent (missing) and reported-but-undesired (extra)
--      are both derived by diffing this set against connector_instance; extra
--      ids never match a connector_instance row, so without this watermark
--      they had nowhere to live in the 0044 columns.
-- This one-row-per-gateway table records the last full-list timestamp and the
-- reported spec ids, upserted transactionally with the per-instance stamp. It
-- is observability state refreshed in place, not append-only history (the
-- device-level gateway_manifest table keeps that).

CREATE TABLE IF NOT EXISTS connector_manifest_watermark (
    tenant_id    UUID NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    gateway_id   UUID NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    observed_at  TIMESTAMPTZ NOT NULL,
    reported_ids TEXT[] NOT NULL DEFAULT '{}',
    PRIMARY KEY (tenant_id, gateway_id)
);
