-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0055_connector_manifest_watermark.down.sql reverses the per-gateway
-- connector manifest watermark. The per-instance 0044 columns and the
-- append-only gateway_manifest history are left untouched.

DROP TABLE IF EXISTS connector_manifest_watermark;
