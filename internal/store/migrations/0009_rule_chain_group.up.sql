-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- Rule-chain groups separate the stable identity of a chain from its
-- immutable version rows. Bindings (device profile and device) point at a
-- group, so activating a new version never invalidates a binding. Each
-- tenant has at most one default group, which keeps the legacy tenant
-- root-chain behavior for profiles and devices without an explicit binding.

CREATE TABLE IF NOT EXISTS rule_chain_group (
    id UUID PRIMARY KEY,
    tenant_id UUID NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    is_default BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, name)
);

CREATE UNIQUE INDEX IF NOT EXISTS rule_chain_group_default_uniq
    ON rule_chain_group (tenant_id) WHERE is_default;

-- Backfill one group per distinct (tenant_id, name). Existing chains were
-- all tenant root chains, so their groups become the tenant default.
INSERT INTO rule_chain_group (id, tenant_id, name, is_default)
SELECT gen_random_uuid(), tenant_id, name, BOOL_OR(is_root)
FROM rule_chain
GROUP BY tenant_id, name;

ALTER TABLE rule_chain ADD COLUMN IF NOT EXISTS chain_group_id UUID;

UPDATE rule_chain rc
SET chain_group_id = g.id
FROM rule_chain_group g
WHERE g.tenant_id = rc.tenant_id AND g.name = rc.name;

ALTER TABLE rule_chain ALTER COLUMN chain_group_id SET NOT NULL;

ALTER TABLE rule_chain
    ADD CONSTRAINT rule_chain_group_fkey
    FOREIGN KEY (chain_group_id) REFERENCES rule_chain_group(id) ON DELETE CASCADE;

-- Optional profile-level binding. NULL means: use the tenant default chain.
ALTER TABLE device_profile ADD COLUMN IF NOT EXISTS rule_chain_group_id UUID
    REFERENCES rule_chain_group(id) ON DELETE SET NULL;

-- Optional device-level override. NULL means: follow the profile binding.
-- A device binding fully replaces the profile chain, per ADR-0028.
ALTER TABLE device ADD COLUMN IF NOT EXISTS rule_chain_group_id UUID
    REFERENCES rule_chain_group(id) ON DELETE SET NULL;
