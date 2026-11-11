-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- In-app notifications. One row is recorded per alarm event that passes
-- the notification frequency rules (ADR-0019), independently of the
-- delivery channels (webhook/SMTP). The row backs the notification inbox
-- and is simultaneously published on the nl.notification Core NATS
-- subjects for WebSocket push.

CREATE TABLE IF NOT EXISTS notification (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    alarm_id UUID,
    device_id UUID,
    severity TEXT NOT NULL CHECK (severity IN ('CRITICAL', 'MAJOR', 'MINOR', 'WARNING')),
    title TEXT NOT NULL,
    body TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL DEFAULT 'NEW' CHECK (state IN ('NEW', 'READ')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    read_at TIMESTAMPTZ
);

-- Inbox queries filter by tenant and unread state, newest first.
CREATE INDEX IF NOT EXISTS notification_tenant_time_idx
    ON notification (tenant_id, state, created_at DESC);
