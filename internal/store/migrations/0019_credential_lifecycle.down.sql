-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Reverse of 0019: drop the tenant credential epoch and the credential
-- lifecycle columns.

ALTER TABLE tenant
    DROP COLUMN IF EXISTS credential_epoch;

DROP INDEX IF EXISTS device_credential_expiry_idx;
DROP INDEX IF EXISTS device_credential_status_idx;

ALTER TABLE device_credential
    DROP COLUMN IF EXISTS updated_at,
    DROP COLUMN IF EXISTS warn_level,
    DROP COLUMN IF EXISTS expires_at,
    DROP COLUMN IF EXISTS effective_at,
    DROP COLUMN IF EXISTS status;
