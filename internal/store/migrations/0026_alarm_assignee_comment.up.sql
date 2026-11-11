-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Alarm assignee and comment support (ADR-0019 two-level model).
--
-- These additions implement the lightweight ticket loop the ADR explicitly
-- limits to acknowledgment, assignment and comments: there is intentionally
-- no attachment storage or full work-order system. The migration only adds
-- nullable/append-only state, so existing alarms and historical audit rows
-- remain unchanged.

ALTER TABLE alarm
    ADD COLUMN IF NOT EXISTS assignee_id UUID REFERENCES app_user(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS alarm_comment (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL,
    alarm_id UUID NOT NULL REFERENCES alarm(id) ON DELETE CASCADE,
    author_id UUID NOT NULL REFERENCES app_user(id),
    body TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT alarm_comment_body_check
        CHECK (length(body) BETWEEN 1 AND 2000)
);

CREATE INDEX IF NOT EXISTS alarm_comment_alarm_created_at_idx
    ON alarm_comment (alarm_id, created_at);
