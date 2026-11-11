-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Remove TOTP MFA state and recovery codes.

DROP TABLE IF EXISTS mfa_recovery_code;

ALTER TABLE app_user
    DROP COLUMN IF EXISTS totp_pending_secret_cipher,
    DROP COLUMN IF EXISTS totp_secret_cipher,
    DROP COLUMN IF EXISTS totp_enabled_at;
