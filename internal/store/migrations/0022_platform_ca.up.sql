-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0022 introduces the platform trust-anchor table for device mTLS
-- (ADR-0009). Rows are CA certificates: the built-in CA created by
-- "pki init" is upserted by the server, and platform administrators
-- import third-party CAs whose issued device certificates are then
-- trusted. Private CA keys never enter this table or the database.

CREATE TABLE IF NOT EXISTS platform_ca (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    label              TEXT NOT NULL,
    subject_cn         TEXT NOT NULL DEFAULT '',
    issuer_cn          TEXT NOT NULL DEFAULT '',
    fingerprint_sha256 TEXT NOT NULL,
    pem                TEXT NOT NULL,
    builtin            BOOLEAN NOT NULL DEFAULT false,
    status             TEXT NOT NULL DEFAULT 'ACTIVE',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT platform_ca_fingerprint_uniq UNIQUE (fingerprint_sha256),
    CONSTRAINT platform_ca_status_check CHECK (status IN ('ACTIVE', 'DISABLED'))
);
