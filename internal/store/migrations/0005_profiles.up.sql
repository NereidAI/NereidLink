-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Device profiles group declarative telemetry descriptors. A device may
-- reference one profile; devices without a profile continue to publish
-- untransformed values.
CREATE TABLE IF NOT EXISTS device_profile (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    name TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

ALTER TABLE device ADD COLUMN IF NOT EXISTS profile_id UUID
    REFERENCES device_profile(id) ON DELETE SET NULL;

-- Measurement descriptors declare the raw-to-engineering transformation,
-- engineering unit, precision, valid range and descriptor version. The
-- version travels with stored rows so recalibration does not alter history.
CREATE TABLE IF NOT EXISTS measurement_descriptor (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id),
    profile_id UUID NOT NULL REFERENCES device_profile(id) ON DELETE CASCADE,
    measurement_key TEXT NOT NULL,
    unit TEXT NOT NULL DEFAULT '',
    transform_type TEXT NOT NULL CHECK (
        transform_type IN ('identity', 'linear', 'table', 'expression', 'enum')
    ),
    scale DOUBLE PRECISION NOT NULL DEFAULT 1,
    offset_value DOUBLE PRECISION NOT NULL DEFAULT 0,
    breakpoints JSONB NOT NULL DEFAULT '[]',
    enum_map JSONB NOT NULL DEFAULT '{}',
    expression TEXT NOT NULL DEFAULT '',
    precision INTEGER NOT NULL DEFAULT -1,
    valid_min DOUBLE PRECISION,
    valid_max DOUBLE PRECISION,
    version INTEGER NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (profile_id, measurement_key)
);

CREATE INDEX IF NOT EXISTS measurement_descriptor_tenant_idx
    ON measurement_descriptor (tenant_id);

-- Device-level calibration overrides tune a profile descriptor for one
-- device without cloning the whole profile.
CREATE TABLE IF NOT EXISTS device_measurement_override (
    device_id UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    measurement_key TEXT NOT NULL,
    scale DOUBLE PRECISION NOT NULL DEFAULT 1,
    offset_value DOUBLE PRECISION NOT NULL DEFAULT 0,
    valid_min DOUBLE PRECISION,
    valid_max DOUBLE PRECISION,
    precision INTEGER NOT NULL DEFAULT -1,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (device_id, measurement_key)
);

ALTER TABLE ts_telemetry ADD COLUMN IF NOT EXISTS raw_value DOUBLE PRECISION;
ALTER TABLE ts_telemetry ADD COLUMN IF NOT EXISTS unit TEXT NOT NULL DEFAULT '';
ALTER TABLE ts_telemetry ADD COLUMN IF NOT EXISTS descriptor_version INTEGER;
ALTER TABLE ts_telemetry ADD COLUMN IF NOT EXISTS v_string TEXT;
