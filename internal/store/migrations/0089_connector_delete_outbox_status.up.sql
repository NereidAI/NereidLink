-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0089 persists explicit connector deletes independently of desired rows and
-- adds the optional NE job/manager readback projection. A tombstone is written
-- in the same transaction as desired-row removal. It has no expiry: a newer
-- same-spec desired revision supersedes it; transport/manifest signals never do.
CREATE TABLE connector_delete_outbox (
    tenant_id       UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id      UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    spec_id         UUID NOT NULL,
    device_id       UUID,
    connector_type  TEXT NOT NULL DEFAULT '',
    type_version    TEXT NOT NULL DEFAULT '',
    delete_revision BIGINT NOT NULL CHECK (delete_revision > 0),
    state           TEXT NOT NULL DEFAULT 'PENDING'
                    CHECK (state IN ('PENDING', 'DISPATCHED')),
    attempt_count   BIGINT NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_attempt_at TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, gateway_id, spec_id)
);

-- Deliberately no FK from spec_id/device_id to the desired/device tables:
-- the delete identity must survive removal of both rows.

-- Replay is bounded and fair: never-attempted tombstones come first, then
-- the least recently attempted ones. Because there is no delete-ACK contract,
-- ordering by created_at alone would let a retained old backlog permanently
-- starve newer explicit deletes.
CREATE INDEX connector_delete_outbox_replay_idx
    ON connector_delete_outbox (gateway_id, last_attempt_at NULLS FIRST, created_at, spec_id);

-- JSONB preserves presence independently for every stats field. NULL and {}
-- both mean unavailable; {"success_total":0} is a real zero reading.
ALTER TABLE connector_instance ADD COLUMN reported_stats JSONB;
ALTER TABLE connector_instance ADD COLUMN observed_revision BIGINT;
ALTER TABLE connector_instance ADD COLUMN observed_hash TEXT NOT NULL DEFAULT '';
