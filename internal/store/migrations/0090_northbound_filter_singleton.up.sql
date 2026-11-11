-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0090 enforces the NE node-runtime contract: one gateway-level
-- edge-northbound-filter connector owns the primary telemetry publisher for
-- a tenant/gateway. Bound sub-device connectors are unrestricted by this
-- partial index; business deployment reconciles the singleton's content.
CREATE UNIQUE INDEX IF NOT EXISTS connector_instance_one_northbound_filter
    ON connector_instance (tenant_id, gateway_id)
    WHERE connector_type = 'edge-northbound-filter' AND device_id IS NULL;
