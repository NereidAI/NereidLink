-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0046_discovery_candidate.up.sql adds the human-approval queue for gateway
-- auto-provisioning (ADR-0026). Migration 0045 made auto-provisioning policy
-- per tenant/gateway; this migration adds a third outcome besides immediate
-- create/reject: when the effective approval mode is "review", a discovery
-- candidate that would otherwise create a new device is instead parked here
-- as PENDING until an operator approves or rejects it.
--
-- The queue is deliberately append-only for auditability: a decision never
-- mutates the row in place to delete the request, it moves it to
-- APPROVED/REJECTED/EXPIRED with actor/timestamps. Repeated discovery of the
-- same (gateway, local_id) while a PENDING row exists is idempotent: ON
-- CONFLICT DO NOTHING keeps the oldest request and the uplink returns
-- "pending" for it. A decision is durable; a later re-discovery after
-- approval resolves through the normal device table as "existing".

CREATE TABLE IF NOT EXISTS discovery_candidate (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id      uuid NOT NULL REFERENCES device(id) ON DELETE CASCADE,

    -- Gateway-local stable key (unit id / address / node id). Combined with
    -- the gateway canonical name to build the deterministic device name.
    local_id        text NOT NULL,
    -- Desired canonical name override; empty means "<gateway>/<local_id>".
    candidate_name  text NOT NULL DEFAULT '',
    -- Resolved profile UUID at enqueue time; empty = no profile.
    profile_id      uuid REFERENCES device_profile(id) ON DELETE SET NULL,
    -- Opaque discovery source/type from the uplink frame.
    source_type     text NOT NULL DEFAULT '',

    state           text NOT NULL DEFAULT 'PENDING',

    -- Set when an approval creates the device.
    device_id       uuid REFERENCES device(id) ON DELETE SET NULL,
    decision_reason text NOT NULL DEFAULT '',
    -- Actor identity string (user UUID or service account) of the decider.
    decided_by      text,
    decided_at      timestamptz,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT discovery_candidate_state_check
        CHECK (state IN ('PENDING','APPROVED','REJECTED','EXPIRED')),
    -- local_id is non-empty (the uplink already rejects empties, but keep the
    -- invariant at the table boundary too).
    CONSTRAINT discovery_candidate_local_check CHECK (length(local_id) > 0)
);

-- Idempotency: at most one live PENDING request per gateway-local key. The
-- partial index lets the same local_id be re-discovered after a terminal
-- decision without colliding, while ON CONFLICT parks duplicates during
-- review.
CREATE UNIQUE INDEX IF NOT EXISTS discovery_candidate_one_pending
    ON discovery_candidate (gateway_id, local_id)
    WHERE state = 'PENDING';

-- Common list/filter access paths.
CREATE INDEX IF NOT EXISTS discovery_candidate_tenant_state_idx
    ON discovery_candidate (tenant_id, state, created_at DESC);
CREATE INDEX IF NOT EXISTS discovery_candidate_gateway_idx
    ON discovery_candidate (gateway_id);

-- Policy tri-state approval mode (migration 0045 extension). NULL inherits
-- the next-less-specific level; the deployment default is "auto". "review"
-- parks would-create candidates in the table above instead of creating them;
-- "auto" preserves immediate-create behavior. enabled=false (resolved
-- separately) still rejects every candidate before this mode matters.
ALTER TABLE discovery_policy ADD COLUMN IF NOT EXISTS approval_mode text;
ALTER TABLE discovery_policy
    ADD CONSTRAINT discovery_policy_approval_mode_check
    CHECK (approval_mode IS NULL OR approval_mode IN ('auto','review'));

COMMENT ON TABLE discovery_candidate IS
    'Human-approval queue for gateway discovery auto-provisioning (ADR-0026, migration 0046). PENDING rows are created only when effective policy approval_mode=review; an operator approves (creates the device) or rejects.';
