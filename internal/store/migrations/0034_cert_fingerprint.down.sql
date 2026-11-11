-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0034_cert_fingerprint.down.sql removes the reuse-detection column and
-- indexes. Existing credentials are left intact otherwise.

DROP INDEX IF EXISTS device_credential_x509_pending_idx;
DROP INDEX IF EXISTS device_credential_x509_fp_uniq;
ALTER TABLE device_credential DROP COLUMN IF EXISTS cert_fingerprint;
