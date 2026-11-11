-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0045_discovery_policy.up.sql adds per-tenant and per-gateway overrides for
-- gateway auto-provisioning policy (ADR-0026). Until now auto-provisioning was
-- gated by a single deployment-level auto_provision.* config block
-- (internal/discovery). This table lets the control plane scope that policy:
--
--   * a row with gateway_id NULL is the TENANT default;
--   * a row with gateway_id set is a GATEWAY override, the most specific.
--
-- Effective resolution for one frame is gateway row -> tenant row ->
-- deployment YAML default. Rows exist only where an operator set an override
-- (the retention_policy 0033 pattern), so most tenants keep inheriting the
-- deployment default and no data backfill is needed.
--
-- Nullable value columns use a tri-state semantics: NULL means "inherit the
-- next level for this field", while a present value (including false / 0 / '')
-- is an explicit setting. This lets a gateway override only, say, the caps
-- while inheriting enabled/default_profile from the tenant.

CREATE TABLE IF NOT EXISTS discovery_policy (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid NOT NULL REFERENCES tenant(id) ON DELETE CASCADE,
    -- NULL identifies the tenant-default row; a device id identifies one
    -- gateway. FK is to device (gateways are is_gateway device rows).
    gateway_id      uuid REFERENCES device(id) ON DELETE CASCADE,

    enabled         boolean,
    max_per_cycle   integer,
    max_candidates  integer,
    default_profile text,

    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- At most one tenant-default row per tenant (gateway_id NULL), enforced
    -- by a partial unique index below; a plain UNIQUE cannot target NULL.
    -- At most one override row per (tenant, gateway).
    UNIQUE (tenant_id, gateway_id),
    -- Caps, when set, must be positive.
    CONSTRAINT discovery_policy_cycle_cap_check CHECK (max_per_cycle IS NULL OR max_per_cycle > 0),
    CONSTRAINT discovery_policy_candidates_cap_check CHECK (max_candidates IS NULL OR max_candidates > 0)
);

-- Exactly one tenant-default row per tenant.
CREATE UNIQUE INDEX IF NOT EXISTS discovery_policy_one_tenant_default
    ON discovery_policy (tenant_id) WHERE gateway_id IS NULL;

CREATE INDEX IF NOT EXISTS discovery_policy_tenant_idx
    ON discovery_policy (tenant_id);

COMMENT ON TABLE discovery_policy IS
    'Per-tenant (gateway_id NULL) and per-gateway auto-provisioning policy overrides (ADR-0026). NULL value columns inherit the next-less-specific level; the deployment YAML is the fallback.';
