-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0087_model_segment_freshness.down.sql removes the per-segment models
-- freshness watermark. Inventory rows themselves are untouched: without the
-- watermark the watchdog simply cannot mark segment-level staleness.

DROP TABLE IF EXISTS gateway_model_segment_watermark;
