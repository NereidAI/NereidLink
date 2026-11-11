-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0047_tenant_secret.up.sql adds a per-tenant secret vault for ADR-0026
-- `secret://` credential delivery. Connector desired config may reference a
-- secret by the string `secret://<name>`; NL stores only that reference in the
-- connector spec and resolves it to the plaintext value at the last mile, in
-- the MQTT relay that writes the downlink frame to the owning gateway. Secrets
-- never appear in REST responses, audit details, connector config storage, or
-- the canonical drift hash.
--
-- Values are sealed at rest with AES-256-GCM under a key-encryption key held
-- outside the database (internal/secrets, a file KEK like the PKI device-key
-- wrap), so a database dump does not expose usable credentials. `ciphertext`
-- carries nonce||ciphertext base64-encoded.
--
-- Naming is tenant-local: (tenant_id, name) is unique. DELETE is a hard delete
-- (the secret stops resolving and the gateway receives an empty value); there
-- is no soft-delete because a lingering sealed value adds risk without value.

CREATE TABLE IF NOT EXISTS tenant_secret (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name          text NOT NULL,
    ciphertext    text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- A secret name is a stable lowercase identifier usable inside a
    -- `secret://` reference on the wire.
    CONSTRAINT tenant_secret_name_check CHECK (
        length(name) BETWEEN 1 AND 128 AND name ~ '^[a-zA-Z0-9][a-zA-Z0-9_.-]*$'
    ),
    UNIQUE (tenant_id, name)
);

CREATE INDEX IF NOT EXISTS tenant_secret_tenant_idx
    ON tenant_secret (tenant_id);

COMMENT ON TABLE tenant_secret IS
    'Per-tenant AES-256-GCM sealed secrets referenced as secret://<name> in connector config (ADR-0026, migration 0047). Plaintext is resolved only at the MQTT downlink last mile.';
