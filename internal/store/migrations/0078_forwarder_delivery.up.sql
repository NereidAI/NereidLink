-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0078_forwarder_delivery.up.sql adds the bounded per-forwarder delivery log
-- (ADR-0018, Stage C). Each real delivery attempt appends one row capturing
-- the receiver return code (e.g. HTTP status), classification and attempt
-- number, so an operator can see delivery results that are otherwise
-- unobservable because settlement happens asynchronously after enqueue.
--
-- Bounding: the table holds no full payload (the envelope id joins to the
-- DLQ row and live trace), and the worker prunes it by age and by a
-- per-forwarder row cap; it therefore cannot grow without limit. It stays
-- off the ingest hot path and never blocks a delivery.

CREATE TABLE IF NOT EXISTS forwarder_delivery (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    forwarder_id UUID NOT NULL REFERENCES forwarder(id) ON DELETE CASCADE,
    -- Joins the attempt to its envelope/trace without storing the body.
    envelope_id TEXT NOT NULL DEFAULT '',
    device_id TEXT NOT NULL DEFAULT '',
    -- Stable classification labels; a CHECK keeps writes honest.
    status TEXT NOT NULL
        CHECK (status IN ('delivered', 'retry', 'terminal')),
    -- Receiver return code (e.g. HTTP status); 0 when no response arrived.
    code INTEGER NOT NULL DEFAULT 0,
    reason TEXT NOT NULL DEFAULT '',
    attempts INTEGER NOT NULL DEFAULT 0,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS forwarder_delivery_tenant_idx
    ON forwarder_delivery (tenant_id, occurred_at DESC);

CREATE INDEX IF NOT EXISTS forwarder_delivery_forwarder_idx
    ON forwarder_delivery (forwarder_id, occurred_at DESC);
