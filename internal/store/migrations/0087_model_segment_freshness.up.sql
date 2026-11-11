-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0087_model_segment_freshness.up.sql adds a per-segment freshness watermark
-- for the optional full-replacement manifest "models" segment (ADR-0039).
--
-- The shared gw-manifest lease only proves that SOME manifest frame arrived:
-- an edge which keeps reporting devices/connectors but stops including the
-- models segment keeps renewing the frame lease, so its (now stale) model
-- inventory was never flagged. This table records the last time a models
-- segment -- present, possibly an explicit "[]" -- was reconciled for each
-- gateway. The modelinventory watchdog marks inventory stale when this
-- watermark is older than the threshold. Gateways that never reported the
-- segment have no row and keep the old "leave untouched" behaviour (older
-- firmware, un-negotiated inference).

CREATE TABLE IF NOT EXISTS gateway_model_segment_watermark (
    tenant_id      UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id     UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    last_models_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, gateway_id)
);

-- The sweep scans for overdue watermarks; index the scanned column.
CREATE INDEX IF NOT EXISTS gateway_model_segment_watermark_time_idx
    ON gateway_model_segment_watermark (last_models_at);

-- Backfill watermarks for inventory that existed before this migration so
-- the segment-freshness watchdog covers already-deployed gateways on the
-- first sweep instead of waiting for their next models segment. The last
-- reconcile timestamp is exactly the last observed segment time; a gateway
-- whose edge silently stopped sending the segment is therefore immediately
-- overdue. Inventory only exists for inference-negotiated gateways.
INSERT INTO gateway_model_segment_watermark (tenant_id, gateway_id, last_models_at)
SELECT tenant_id, gateway_id, max(updated_at)
  FROM gateway_model_inventory
 GROUP BY tenant_id, gateway_id
 ON CONFLICT (tenant_id, gateway_id) DO NOTHING;
