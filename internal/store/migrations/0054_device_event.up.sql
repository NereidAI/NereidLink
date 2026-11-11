-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0054_device_event.up.sql introduces the general device domain-event
-- channel backing the direct-device MQTT v1/me/events uplink. Until now that
-- topic was ACL-accepted but had no handler (L-067): events were silently
-- dropped. Only direct devices are covered; there is no v1/gateway/events
-- topic in the protocol catalog. This is an append-only, platform-agnostic
-- ledger: the platform stores and routes events but attaches no business
-- meaning to event_type or payload (tenants define those), consistent with
-- the zero-business-logic constraint.
--
-- It is a normal heap table like alarm/audit rather than a Timescale
-- hypertable: events are low-frequency lifecycle/domain signals, not the
-- high-volume measurement stream that owns ts_telemetry. Retention is the
-- existing "event" domain (default 1095 days), enforced by timestamp.
CREATE TABLE IF NOT EXISTS device_event (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id   UUID NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    device_id   UUID NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    time        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- event_type is the tenant-named signal (e.g. "door_opened"). The
    -- platform treats it as an opaque label; it never branches on it.
    event_type  TEXT NOT NULL,
    -- severity is transport envelope metadata carried verbatim, not a
    -- platform rule; default keeps an unspecified severity valid.
    severity    TEXT NOT NULL DEFAULT 'info'
                  CHECK (severity IN ('info','warning','error','critical')),
    payload     JSONB NOT NULL DEFAULT '{}'::jsonb
);

-- Per-device timeline (device detail view) and per-tenant sweep/listing.
CREATE INDEX IF NOT EXISTS device_event_device_time_idx
    ON device_event (tenant_id, device_id, time DESC);
CREATE INDEX IF NOT EXISTS device_event_tenant_time_idx
    ON device_event (tenant_id, time DESC);
-- Optional type filtering without forcing every query to scan the timeline.
CREATE INDEX IF NOT EXISTS device_event_type_time_idx
    ON device_event (tenant_id, event_type, time DESC);
