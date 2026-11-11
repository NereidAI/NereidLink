-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0019 implements the device credential lifecycle from ADR-0009:
-- per-credential enable/disable state, an effective/expiry validity
-- window and a per-tenant credential epoch used for emergency bulk
-- invalidation. Rotation follows an overlap window (register the new
-- credential while the old one still validates) then disable the old.

ALTER TABLE device_credential
    ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'ACTIVE'
        CHECK (status IN ('ACTIVE', 'DISABLED', 'EXPIRED')),
    ADD COLUMN IF NOT EXISTS effective_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ,
    -- warn_level records the strongest expiry warning already raised so
    -- the background scanner does not emit duplicate alarms.
    ADD COLUMN IF NOT EXISTS warn_level TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT now();

-- Existing credentials were always usable from creation and never
-- expired, so they backfill with an explicit effective time.
UPDATE device_credential SET effective_at = created_at WHERE effective_at IS NULL;

CREATE INDEX IF NOT EXISTS device_credential_status_idx
    ON device_credential (status);

CREATE INDEX IF NOT EXISTS device_credential_expiry_idx
    ON device_credential (expires_at)
    WHERE status = 'ACTIVE';

-- Per-tenant credential epoch. Bumping it invalidates every cached
-- credential decision for the tenant (ADR-0009 emergency revocation).
ALTER TABLE tenant
    ADD COLUMN IF NOT EXISTS credential_epoch BIGINT NOT NULL DEFAULT 0;
