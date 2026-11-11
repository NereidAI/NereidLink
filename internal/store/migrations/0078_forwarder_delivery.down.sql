-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0078_forwarder_delivery.down.sql removes the per-forwarder delivery log.
-- Forwarder configuration and parked dead letters are retained; only the
-- operational attempt history is discarded.

DROP TABLE IF EXISTS forwarder_delivery;
