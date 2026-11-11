-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0086_edge_model_inference.up.sql adds the cloud Phase A surface for edge
-- model inference (ADR-0039). No model bytes pass through NereidLink; the
-- gateway reports its local model inventory in the optional full-replacement
-- manifest "models" segment, and the platform stores/reconciles that view.
--
-- New objects:
--   * device.agent_inference            negotiated hello inference capability
--   * device_profile.is_system          marker for the seeded task-family
--                                       system profiles (tenant-visible, not
--                                       operator-editable/deletable)
--   * gateway_model_inventory           one model present on one gateway
--   * gateway_model_inventory_event     durable audit trail (prunes)
--   * the detection/v1 system profile + point descriptors, seeded for every
--     existing tenant (new tenants get the same seed from the provisioning
--     reconciler, one Go/SQL definition of the point set).

-- 1. Negotiated inference capability stamped at hello, e.g.
--    {"contract":"model/1","backends":["kserve","openai"]}. An empty object
--    means the gateway did not negotiate inference and takes no part in any
--    inference semantics. Kept next to agent_features/agent_profile_formats.
ALTER TABLE device ADD COLUMN IF NOT EXISTS agent_inference
    JSONB NOT NULL DEFAULT '{}'::jsonb;

-- 2. System marker on the profile library. System profiles are shipped by the
--    platform (task-family point contracts), visible to every tenant, and
--    protected from operator edit/delete in the service layer. They still
--    carry source='manual' because the source CHECK enum predates this flag;
--    is_system is the authoritative discriminator.
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS is_system
    BOOLEAN NOT NULL DEFAULT FALSE;

-- 3. One model present on one gateway. The inventory key is
--    (tenant, gateway, model_id, version); revision is the build id and is
--    TEXT (e.g. "76937"), unlike connector/profile BIGINT revisions. The
--    frame is authoritative full state: a model absent from a present list is
--    hard-deleted (with an audit row), unlike devices which go OFFLINE.
--    `stale` marks rows whose gw-manifest lease expired: they are kept (not
--    pruned) until the gateway reports again. failed_alarm is the atomic
--    sweep claim flag for the FAILED alarm (crash-safe at-most-once raise).
CREATE TABLE IF NOT EXISTS gateway_model_inventory (
    tenant_id       UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id      UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    model_id        TEXT NOT NULL,
    version         TEXT NOT NULL,
    revision        TEXT NOT NULL DEFAULT '',
    task            TEXT NOT NULL DEFAULT '',
    family          TEXT NOT NULL DEFAULT '',
    backend         TEXT NOT NULL DEFAULT '',
    model_name      TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'VALIDATING'
                        CHECK (state IN ('EXTRACTING','VALIDATING','READY',
                                         'FAILED','REMOVED')),
    reason          TEXT NOT NULL DEFAULT '',
    labels          JSONB NOT NULL DEFAULT '[]'::jsonb,
    active_bindings INTEGER NOT NULL DEFAULT 0,
    stale           BOOLEAN NOT NULL DEFAULT FALSE,
    stale_at        TIMESTAMPTZ,
    failed_alarm    BOOLEAN NOT NULL DEFAULT FALSE,
    first_seen      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, gateway_id, model_id, version)
);

-- Gateway-scoped listing (the console/wizard) and state scans (sweeper).
CREATE INDEX IF NOT EXISTS gateway_model_inventory_gateway_idx
    ON gateway_model_inventory (gateway_id);
CREATE INDEX IF NOT EXISTS gateway_model_inventory_state_idx
    ON gateway_model_inventory (tenant_id, gateway_id, state);

-- 4. Durable audit trail for inventory changes. Phase A writes one row per
--    model pruned by full-replacement reconciliation; state transitions are
--    reflected by the alarm stream and the live inventory itself.
CREATE TABLE IF NOT EXISTS gateway_model_inventory_event (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id  UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    model_id    TEXT NOT NULL,
    version     TEXT NOT NULL,
    event       TEXT NOT NULL DEFAULT 'pruned'
                  CHECK (event IN ('pruned')),
    last_state  TEXT NOT NULL DEFAULT '',
    observed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS gateway_model_inventory_event_gateway_idx
    ON gateway_model_inventory_event (gateway_id, observed_at);

-- 5. Seed the detection/v1 task-family system profile for every existing
--    tenant. Ids are deterministic (md5-derived UUIDs) so the seed is
--    idempotent and the provisioning reconciler converges to the same rows.
--    The virtual per-binding subdevice references this profile; its points
--    carry ordinary scalar/string telemetry, so ingestion/rule/alarm paths
--    are unchanged. data_type uses the neutral enum (float|string).
INSERT INTO device_profile (id, tenant_id, name, source, protocol,
                            availability, is_system)
SELECT md5(t.id::text || ':device-profile:detection/v1')::uuid,
       t.id, 'detection/v1', 'manual', '', 'active', TRUE
FROM tenant t
ON CONFLICT (tenant_id, name) DO NOTHING;

-- Descriptors for detection/v1 (ADR-0039 §3.4). v_double points map to
-- 'float', v_string points to 'string'. Per-class <label>_count points are
-- optional and derived from a model's labels, so they are intentionally not
-- seeded. The point set here must stay in lockstep with the provisioning
-- reconciler seed (internal/modelinventory seed.go).
INSERT INTO measurement_descriptor
    (id, tenant_id, profile_id, measurement_key, data_type, read_write,
     transform_type, unit)
SELECT md5(p.id::text || ':descriptor:' || d.key)::uuid,
       p.tenant_id, p.id, d.key, d.data_type, 'R', 'identity', d.unit
FROM device_profile p
CROSS JOIN (VALUES
    ('detection_count',  'float',  ''),
    ('top1_label',       'string', ''),
    ('top1_score',       'float',  ''),
    ('infer_latency_ms', 'float',  'ms'),
    ('detections_json',  'string', '')
) AS d(key, data_type, unit)
WHERE p.name = 'detection/v1' AND p.is_system = TRUE
ON CONFLICT (profile_id, measurement_key) DO NOTHING;
