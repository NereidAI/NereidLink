-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Presence history: one row per connection state boundary. The current
-- state lives in NATS KV; this hypertable supports availability analysis
-- and audit only and is never read on the hot path.

CREATE TABLE IF NOT EXISTS ts_presence (
    time TIMESTAMPTZ NOT NULL,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    gateway_id UUID,
    state TEXT NOT NULL CHECK (state IN ('ONLINE', 'OFFLINE')),
    reason TEXT NOT NULL,
    session_id TEXT NOT NULL
);

SELECT create_hypertable('ts_presence', 'time', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS ts_presence_device_time_idx
    ON ts_presence (device_id, time DESC);
