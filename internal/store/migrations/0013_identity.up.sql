-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0013 adds the local identity fallback required by ADR-0021 for
-- deployments without an external OIDC provider: users with argon2id
-- password hashes and refresh-token sessions (only hashes stored, never
-- the token itself). OIDC/JIT users later reuse the same tables with a
-- nullable password_hash.
--
-- The identifier uniqueness is per tenant, per the ADR: the same email
-- may identify accounts in different tenants. Platform-level admins
-- have tenant_id NULL.

CREATE TABLE IF NOT EXISTS app_user (
    id UUID PRIMARY KEY,
    tenant_id UUID REFERENCES tenant(id),
    email TEXT NOT NULL,
    phone TEXT NOT NULL DEFAULT '',
    display_name TEXT NOT NULL DEFAULT '',
    password_hash TEXT NOT NULL DEFAULT '',
    is_platform_admin BOOLEAN NOT NULL DEFAULT FALSE,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    disabled BOOLEAN NOT NULL DEFAULT FALSE,
    must_change_password BOOLEAN NOT NULL DEFAULT FALSE,
    -- Lockout state: 10 failures within a 15-minute window locks the
    -- account until locked_until. first_failed_at anchors the window.
    failed_login_count INTEGER NOT NULL DEFAULT 0,
    first_failed_at TIMESTAMPTZ,
    locked_until TIMESTAMPTZ,
    last_login_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- NULL tenant values are distinct in a plain UNIQUE constraint, which
-- is exactly what platform admins need; a plain unique index therefore
-- expresses per-tenant email uniqueness correctly.
CREATE UNIQUE INDEX IF NOT EXISTS app_user_tenant_email_uniq
    ON app_user (tenant_id, email);

CREATE TABLE IF NOT EXISTS user_session (
    id UUID PRIMARY KEY,
    user_id UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    -- bytea: the SHA-256 digest is raw bytes, not valid UTF-8 text.
    refresh_token_hash BYTEA NOT NULL,
    ip TEXT NOT NULL DEFAULT '',
    user_agent TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS user_session_user_idx
    ON user_session (user_id);

CREATE INDEX IF NOT EXISTS user_session_expires_idx
    ON user_session (expires_at);
