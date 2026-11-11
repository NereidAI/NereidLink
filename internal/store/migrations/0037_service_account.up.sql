-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0037_service_account.up.sql adds ADR-0021 machine identities: tenant-local
-- OAuth2 client_credentials service accounts that replace long-lived
-- username/password automation.
--
-- A service account owns a stable public client_id and one or two secret
-- rows. Two rows are deliberately allowed: during rotation the old
-- PRIMARY is demoted to ROTATING and the new secret becomes PRIMARY, so an
-- application can deploy its new secret while in-flight workers still
-- present the old one. The next rotation deletes the ROTATING row, which
-- bounds stored credentials at two per account.
--
-- Secret values are never stored: service_account_secret.secret_hash is an
-- argon2id PHC string produced by identity.HashSecret, the same hasher
-- used for user passwords and device PASSWORD credentials.
--
-- scopes is the set of ADR-0022 permission points the issued token may
-- exercise in the owning tenant. The CHECK enumerates the version-fixed
-- point catalog: a token can never carry a string the RBAC layer does not
-- understand, and '*' is the tenant-admin wildcard.

CREATE TABLE service_account (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    client_id     TEXT NOT NULL UNIQUE,
    name          TEXT NOT NULL,
    scopes        TEXT[] NOT NULL DEFAULT '{}',
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    last_used_at  TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT service_account_name_chk
        CHECK (char_length(name) BETWEEN 1 AND 200),

    -- Every scope must be a known permission point; a malformed scope would
    -- silently grant nothing, so reject it at write time instead. The
    -- contained-by operator (<@) is the no-subquery form of "every element
    -- of scopes is in the whitelist" (a CHECK expression cannot SELECT).
    CONSTRAINT service_account_scopes_chk CHECK (
        scopes <@ ARRAY[
            '*',
            'device:read', 'device:write', 'device:command',
            'credential:read', 'credential:write',
            'profile:read', 'profile:write',
            'rulechain:read', 'rulechain:write',
            'alarm:read', 'alarm:ack', 'alarm:assign', 'alarm:comment',
            'hook:read', 'hook:write',
            'dashboard:read', 'dashboard:write',
            'escalation:read', 'escalation:write',
            'stats:read', 'presence:read',
            'notification:read',
            'quota:read', 'retention:read',
            'serviceaccount:read', 'serviceaccount:write'
        ]::text[]
    )
);

-- Tenant administration lists accounts by tenant; client_id lookups during
-- the token grant use the UNIQUE constraint above.
CREATE INDEX service_account_tenant_idx
    ON service_account (tenant_id);

CREATE TABLE service_account_secret (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    service_account_id UUID NOT NULL
        REFERENCES service_account(id) ON DELETE CASCADE,
    secret_hash        TEXT NOT NULL,
    -- PRIMARY is the current secret; ROTATING is the previous one kept
    -- alive for overlap during secret deployment.
    label              TEXT NOT NULL,
    expires_at         TIMESTAMPTZ,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT service_account_secret_label_chk
        CHECK (label IN ('PRIMARY', 'ROTATING')),
    -- At most one row per label, which both enforces the two-secret bound
    -- and makes label-based rotation updates conflict-free.
    CONSTRAINT service_account_secret_unique
        UNIQUE (service_account_id, label)
);
