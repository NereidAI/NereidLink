-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0033_retention_policy.up.sql introduces per-tenant, per-domain data
-- retention overrides and a legal-hold exemption (ADR-0005).
--
-- The platform defaults live in code (see internal/retention); a row
-- exists only when an operator shortens a window or places a tenant on
-- legal hold. Shortening a window changes the deletion cutoff only and
-- never removes rows synchronously: a background enforcer applies the
-- cutoff later. retention_days = 0 means "keep indefinitely" under
-- automatic enforcement.

CREATE TABLE IF NOT EXISTS retention_policy (
    tenant_id      uuid        NOT NULL
        REFERENCES tenant (id) ON DELETE CASCADE,
    domain         text        NOT NULL,
    retention_days integer     NOT NULL,
    legal_hold     boolean     NOT NULL DEFAULT false,
    updated_at     timestamptz NOT NULL DEFAULT now(),

    -- domain is one of the internal/retention domain constants.
    CONSTRAINT retention_policy_domain_check
        CHECK (domain IN ('telemetry', 'event', 'alarm', 'audit')),
    CONSTRAINT retention_policy_days_check
        CHECK (retention_days >= 0),

    PRIMARY KEY (tenant_id, domain)
);

-- Comments are the contract for future readers of the raw schema.
COMMENT ON TABLE retention_policy IS
    'Per-tenant retention overrides and legal-hold flags (ADR-0005).';
COMMENT ON COLUMN retention_policy.retention_days IS
    'Automatic-delete age in days; 0 keeps data indefinitely.';
COMMENT ON COLUMN retention_policy.legal_hold IS
    'When true, automatic deletion is suspended for this tenant/domain.';
