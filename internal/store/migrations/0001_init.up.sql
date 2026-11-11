-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

CREATE EXTENSION IF NOT EXISTS timescaledb;

CREATE TABLE IF NOT EXISTS tenant (
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS device (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    canonical_name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, canonical_name)
);

CREATE TABLE IF NOT EXISTS device_credential (
    id UUID PRIMARY KEY,
    device_id UUID NOT NULL REFERENCES device(id),
    type TEXT NOT NULL,
    credentials_id TEXT NOT NULL UNIQUE,
    credentials_value TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS ts_telemetry (
    time TIMESTAMPTZ NOT NULL,
    tenant_id UUID NOT NULL,
    device_id UUID NOT NULL,
    measurement_key TEXT NOT NULL,
    v_double DOUBLE PRECISION,
    quality TEXT NOT NULL DEFAULT 'GOOD'
);

SELECT create_hypertable('ts_telemetry', 'time', if_not_exists => TRUE);

CREATE INDEX IF NOT EXISTS ts_telemetry_device_time_idx
    ON ts_telemetry (device_id, time DESC);
