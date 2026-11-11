-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0061_profile_library.up.sql turns device_profile into the tenant-level,
-- vendor-neutral device-profile library described in ADR-0032. Profiles can
-- now arrive from gateways (edge uplink), paste-import, or manual creation;
-- they carry a protocol family hint, the verbatim reported payload and a
-- content hash for drift detection.
--
-- Every new column is nullable / defaulted so existing operator-created
-- profiles keep working unchanged: they are treated as source='manual',
-- availability='active' with no protocol and no edge payload.

-- 1. Library metadata on device_profile.
--    source          where the profile content came from
--    protocol        open-vocabulary protocol family (e.g. 'modbus'); empty
--                    means unknown and disables strong protocol checks
--    edge_payload    verbatim neutral profile JSON from the edge/import
--    content_hash    canonical sha256 of that JSON, for drift detection
--    availability    'discovered' (only reported, unreferenced) vs 'active'
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS source
    TEXT NOT NULL DEFAULT 'manual'
    CHECK (source IN ('edge', 'import', 'manual'));
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS protocol
    TEXT NOT NULL DEFAULT '';
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS edge_payload
    JSONB;
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS content_hash
    TEXT NOT NULL DEFAULT '';
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS availability
    TEXT NOT NULL DEFAULT 'active'
    CHECK (availability IN ('discovered', 'active'));

CREATE INDEX IF NOT EXISTS device_profile_tenant_avail_idx
    ON device_profile (tenant_id, availability);

-- 2. Neutral spec fields on each point (measurement_descriptor). These are
--    OWNED BY THE EDGE/SPEC and must never be overwritten by operator
--    calibration; they sit alongside the existing NL value-add columns
--    (transform_type/scale/offset_value/enum_map/breakpoints/expression/
--    precision/valid_min/valid_max), which remain operator-owned.
--      data_type     neutral enum bool|int|uint|float|string|binary
--      read_write    'R' / 'RW' / '' (unknown)
--      address       opaque per-protocol addressing object (e.g. Modbus
--                    {primaryTable,startingAddress}); kernel never parses it
--      edge_default  reported default value as text
--      edge_min/max  reported engineering range (distinct from the
--                    operator-owned valid_min/valid_max used for validation)
--      inactive      point removed on the edge but retained for history and
--                    rule references
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS data_type
    TEXT NOT NULL DEFAULT '';
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS read_write
    TEXT NOT NULL DEFAULT '';
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS address
    JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS edge_default
    TEXT NOT NULL DEFAULT '';
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS edge_min
    DOUBLE PRECISION;
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS edge_max
    DOUBLE PRECISION;
ALTER TABLE measurement_descriptor ADD COLUMN IF NOT EXISTS inactive
    BOOLEAN NOT NULL DEFAULT FALSE;

-- 3. Many-to-many provenance: one library profile may be reported by several
--    gateways. gateway_edge_profile (0056) remains the per-gateway raw cache
--    used for manifest reconciliation; this table only records which library
--    profile each gateway contributes and the hash it last reported.
CREATE TABLE IF NOT EXISTS profile_gateway_source (
    profile_id    UUID NOT NULL REFERENCES device_profile(id) ON DELETE CASCADE,
    gateway_id    UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    content_hash  TEXT NOT NULL DEFAULT '',
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, gateway_id)
);

CREATE INDEX IF NOT EXISTS profile_gateway_source_gateway_idx
    ON profile_gateway_source (gateway_id);

-- 4. Open-vocabulary protocol family on connector schemas (ADR-0032 §6). An
--    edge may declare the family a connector type belongs to (e.g. modbus) and
--    the concrete transport keys (modbus-tcp/modbus-rtu). Both are optional;
--    when unset the platform fails open and only warns on family mismatch.
ALTER TABLE connector_schema ADD COLUMN IF NOT EXISTS protocol
    TEXT NOT NULL DEFAULT '';
ALTER TABLE connector_schema ADD COLUMN IF NOT EXISTS transports
    JSONB NOT NULL DEFAULT '[]'::jsonb;

