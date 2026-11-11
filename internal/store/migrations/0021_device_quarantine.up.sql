-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0021 introduces the incident-response quarantine state for devices.
-- A quarantined device keeps its identity, ownership and time series,
-- but its credentials can no longer open an ingress connection. The
-- state is lifted through the ADR-0023 quarantine.release order, which
-- an operator opens from the CLI and the super administrator confirms.

ALTER TABLE device
    ADD COLUMN IF NOT EXISTS quarantined_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS quarantine_reason TEXT NOT NULL DEFAULT '';

-- Replace the state CHECK with one admitting QUARANTINED.
ALTER TABLE device DROP CONSTRAINT device_state_check;
ALTER TABLE device ADD CONSTRAINT device_state_check CHECK (
    state IN ('UNCLAIMED', 'ACTIVE', 'DECOMMISSIONED', 'ARCHIVED', 'QUARANTINED'));
