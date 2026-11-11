-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0039_gateway_link_episode.up.sql persists unplanned gateway link-down
-- episodes for the ADR-0026 link watchdog. Advisories themselves are
-- ephemeral Core NATS messages; this table gives the escalation sweep and
-- the daily digest durable state that survives a process restart.
--
-- Only unplanned outages are recorded. A planned goodbye never raises an
-- alarm and therefore never opens an episode, so planned maintenance cannot
-- page operators or appear in the outage digest.
--
-- Escalation is a single MAJOR -> CRITICAL bump once an episode stays open
-- past the configured threshold (30 minutes by default); escalated_at marks
-- that transition so it fires at most once per episode.

CREATE TABLE gateway_link_episode (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    gateway_id    UUID NOT NULL REFERENCES device(id) ON DELETE CASCADE,
    started_at    TIMESTAMPTZ NOT NULL,
    ended_at      TIMESTAMPTZ,
    down_reason   TEXT NOT NULL,
    alarm_id      UUID NOT NULL,
    escalated     BOOLEAN NOT NULL DEFAULT FALSE,
    escalated_at  TIMESTAMPTZ
);

-- At most one open episode per gateway: a repeated DOWN advisory while an
-- episode is open must not open a second row.
CREATE UNIQUE INDEX gateway_link_episode_open_idx
    ON gateway_link_episode (gateway_id)
    WHERE ended_at IS NULL;

-- The escalation sweep scans open, not-yet-escalated episodes ordered by
-- age.
CREATE INDEX gateway_link_episode_due_idx
    ON gateway_link_episode (started_at)
    WHERE ended_at IS NULL AND escalated = FALSE;

-- The daily digest summarizes episodes that ended within the report window.
CREATE INDEX gateway_link_episode_ended_idx
    ON gateway_link_episode (tenant_id, ended_at DESC)
    WHERE ended_at IS NOT NULL;

-- gateway_link_digest records the UTC calendar day for which each tenant has
-- already received a 24-hour summary, making the once-a-day send idempotent
-- across restarts and across queue-group failover.
CREATE TABLE gateway_link_digest (
    tenant_id   UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    digest_date DATE NOT NULL,
    sent_at     TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (tenant_id, digest_date)
);
