-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0068_step_up_challenge.up.sql persists the short-lived, one-time step-up
-- challenges used to re-authenticate a human operator immediately before a
-- dangerous action (ADR-0035 gateway operations and later protected flows).
-- A challenge is started for one named action bound to the caller's tenant
-- and user, completed by a fresh TOTP code, then consumed exactly once by
-- the protected endpoint. Persistence makes challenges survive a restart
-- and lets a replay (second presentation) be detected and audited.
--
-- Status lifecycle: pending -> verified -> consumed. A row never leaves
-- this path; expired rows are deleted by the periodic sweep.

CREATE TABLE IF NOT EXISTS step_up_challenge (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES app_user(id) ON DELETE CASCADE,
    -- Action names the protected operation this challenge authorizes,
    -- e.g. "edge_operation:exec". Free TEXT by design: the step-up layer
    -- carries no business vocabulary; registries map action -> RBAC point.
    action     TEXT NOT NULL,
    -- Non-sensitive action details shown to the operator (and hashed into
    -- the signed QR payload), e.g. gateway id, operation key.
    details    JSONB NOT NULL DEFAULT '{}'::jsonb,
    details_sha256 CHAR(64) NOT NULL,
    status     TEXT NOT NULL DEFAULT 'pending'
               CHECK (status IN ('pending','verified','consumed')),
    expires_at     TIMESTAMPTZ NOT NULL,
    verified_at    TIMESTAMPTZ,
    consumed_at    TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS step_up_challenge_user_idx
    ON step_up_challenge (tenant_id, user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS step_up_challenge_expiry_idx
    ON step_up_challenge (expires_at);
