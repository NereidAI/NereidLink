-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0015 introduces the append-only audit log and its tamper-evident
-- hash chain (ADR-0029). Rows are never updated or deleted by the
-- application: daily sealing lives in a separate table that records
-- the last event of each day without touching audit rows.
--
-- Chain invariant: every row carries the SHA-256 of its canonical
-- content plus the previous row hash; recomputation detects any
-- insertion, deletion or modification. Writers serialize through a
-- PostgreSQL transaction advisory lock, so concurrent replicas cannot
-- fork the chain.

CREATE TABLE IF NOT EXISTS audit_event (
    seq BIGINT PRIMARY KEY,
    time TIMESTAMPTZ NOT NULL DEFAULT now(),
    tenant_id UUID,
    actor_id UUID,
    actor_name TEXT NOT NULL DEFAULT '',
    event_type TEXT NOT NULL,
    action TEXT NOT NULL DEFAULT '',
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id TEXT NOT NULL DEFAULT '',
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    ip TEXT NOT NULL DEFAULT '',
    prev_hash TEXT NOT NULL DEFAULT '',
    hash TEXT NOT NULL
);

CREATE UNIQUE INDEX IF NOT EXISTS audit_event_hash_uniq
    ON audit_event (hash);

CREATE INDEX IF NOT EXISTS audit_event_time_idx
    ON audit_event (time);

CREATE INDEX IF NOT EXISTS audit_event_type_idx
    ON audit_event (event_type, time);

CREATE INDEX IF NOT EXISTS audit_event_tenant_idx
    ON audit_event (tenant_id, time);

-- audit_daily_seal anchors one completed day at its last event hash.
-- doctor verifies the chain up to each seal; a missing seal or hash
-- mismatch is a tamper alert.
CREATE TABLE IF NOT EXISTS audit_daily_seal (
    day DATE PRIMARY KEY,
    last_seq BIGINT NOT NULL,
    last_hash TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
