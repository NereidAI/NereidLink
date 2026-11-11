-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0034_cert_fingerprint.up.sql stores the SHA-256 leaf fingerprint on
-- X509 credentials so the same client certificate cannot be bound to a
-- second device (ADR-0009 credential-reuse detection).
--
-- The fingerprint is non-sensitive (it is a public hash of the
-- certificate DER) and is filled by the registry service for new rows;
-- existing X509 rows are backfilled once at startup because PostgreSQL
-- cannot parse X.509 in plain SQL. The partial unique index only covers
-- populated rows, and one physical certificate must map to one device.

ALTER TABLE device_credential
    ADD COLUMN IF NOT EXISTS cert_fingerprint TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS device_credential_x509_fp_uniq
    ON device_credential (cert_fingerprint)
    WHERE type = 'X509' AND cert_fingerprint IS NOT NULL;

-- Supports the one-time startup backfill scan of historical X509 rows.
CREATE INDEX IF NOT EXISTS device_credential_x509_pending_idx
    ON device_credential (id)
    WHERE type = 'X509' AND cert_fingerprint IS NULL;
