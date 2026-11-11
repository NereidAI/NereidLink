-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0085_profile_downlink.up.sql adds the device-profile downlink described in
-- ADR-0038. The tenant-level device_profile library is already the single
-- content source of record (ADR-0032); this migration adds the *delivery*
-- half: a content revision for change detection and two tables that record
-- which library profile goes to which gateway and how far each delivery got.
--
-- New device_profile columns are defaulted so existing rows keep working:
-- revision 1 and an empty desired_hash that the service fills on first render.

-- 1. Content revision on the library profile. `revision` is the monotonic
--    version of the EDGE-VISIBLE PROJECTION only (name/protocol/description/
--    labels/points/commands). Operator value-add fields (transform/scale/…)
--    are not part of the projection and never bump it. `desired_hash` is the
--    canonical sha256 of the rendered projection, distinct from the existing
--    `content_hash`, which hashes the verbatim uplink payload.
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS revision
    BIGINT NOT NULL DEFAULT 1;
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS desired_hash
    TEXT NOT NULL DEFAULT '';

-- 2. Explicit distribution list (the shared-library main path): an operator
--    hands a library profile to an arbitrary set of gateways, including ones
--    that never reported it. A profile may be targeted at many gateways and a
--    gateway may receive many profiles.
CREATE TABLE IF NOT EXISTS profile_gateway_target (
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    profile_id  UUID NOT NULL REFERENCES device_profile(id) ON DELETE CASCADE,
    gateway_id  UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (profile_id, gateway_id)
);

CREATE INDEX IF NOT EXISTS profile_gateway_target_gateway_idx
    ON profile_gateway_target (gateway_id);

-- 3. Per-(profile, gateway) delivery state, mirroring connector_instance's
--    desired/reported halves. Targets come from this explicit table plus the
--    implicit set of profiles referenced by the gateway's connector
--    instances; rows are created lazily by the reconcile/publish path.
CREATE TABLE IF NOT EXISTS profile_delivery (
    tenant_id           UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    profile_id          UUID NOT NULL REFERENCES device_profile(id) ON DELETE CASCADE,
    gateway_id          UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,

    desired_revision    BIGINT NOT NULL DEFAULT 0,
    dispatched_revision BIGINT NOT NULL DEFAULT 0,
    applied_revision    BIGINT NOT NULL DEFAULT 0,

    state               TEXT NOT NULL DEFAULT 'PENDING'
                            CHECK (state IN ('PENDING','DISPATCHED','APPLIED',
                                             'FAILED','REJECTED','REMOVING',
                                             'REMOVED')),
    reason              TEXT NOT NULL DEFAULT '',

    pending_alarm       BOOLEAN NOT NULL DEFAULT FALSE,
    failed_alarm        BOOLEAN NOT NULL DEFAULT FALSE,
    failed_critical     BOOLEAN NOT NULL DEFAULT FALSE,

    desired_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    dispatched_at       TIMESTAMPTZ,
    applied_at          TIMESTAMPTZ,
    failed_at           TIMESTAMPTZ,
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (profile_id, gateway_id)
);

CREATE INDEX IF NOT EXISTS profile_delivery_gateway_idx
    ON profile_delivery (gateway_id);
CREATE INDEX IF NOT EXISTS profile_delivery_open_idx
    ON profile_delivery (state)
    WHERE state IN ('PENDING','DISPATCHED','FAILED');

-- 4. The render formats a gateway declared (and the platform enabled) in its
--    last successful hello, e.g. ["nereid-neutral/1","edgex-device-profile/3"].
--    Kept as a fast stamp alongside agent_features; the delivery path picks
--    the first supported preference from it.
ALTER TABLE device ADD COLUMN IF NOT EXISTS agent_profile_formats
    JSONB NOT NULL DEFAULT '[]'::jsonb;
