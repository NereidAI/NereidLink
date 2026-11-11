-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- TOTP multi-factor authentication support (ADR-0021).
--
-- The active TOTP secret is stored only as an application-encrypted value;
-- there is intentionally no plaintext secret column. A separate pending
-- column holds a secret generated during enrollment until the user proves
-- possession with a valid code, at which point it becomes active. Recovery
-- codes are hashed in their own table, so database contents cannot be used
-- directly as a second factor.

ALTER TABLE app_user
    ADD COLUMN IF NOT EXISTS totp_pending_secret_cipher TEXT,
    ADD COLUMN IF NOT EXISTS totp_secret_cipher TEXT,
    ADD COLUMN IF NOT EXISTS totp_enabled_at TIMESTAMPTZ;

CREATE TABLE IF NOT EXISTS mfa_recovery_code (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    code_hash TEXT NOT NULL,
    used_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS mfa_recovery_code_user_idx
    ON mfa_recovery_code (user_id);
