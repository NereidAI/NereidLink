-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Backup markers consumed by `nereidlink doctor` (ADR-0004 backup boundary).
--
-- The platform does not perform managed backups. A customer-owned backup job
-- records that it completed successfully, and doctor only emits an advisory
-- when the newest PostgreSQL marker is missing or older than the operator's
-- accepted recovery-point window. Keeping markers in a normal table means the
-- backup script can append one after pg_dump succeeds without additional
-- state; rows are append-only, so failures never erase prior success history.

CREATE TABLE IF NOT EXISTS backup_marker (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope TEXT NOT NULL,
    succeeded_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    CONSTRAINT backup_marker_scope_check
        CHECK (scope IN ('postgres', 'nats'))
);

CREATE INDEX IF NOT EXISTS backup_marker_scope_succeeded_at_idx
    ON backup_marker (scope, succeeded_at DESC);
