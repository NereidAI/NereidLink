-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0056_gateway_edge_profile.up.sql caches EdgeX device profiles reported by
-- gateways on v1/gateway/profiles. Each row is one named EdgeX profile seen on
-- one gateway; the same profile name reported by two gateways is two rows,
-- because adoption is offered from the concrete reporting gateway.
--
-- The row stores the raw reported profile verbatim (payload JSONB) so resource
-- kinds NL cannot translate today (Binary, arrays, objects) are preserved even
-- though adoption skips them, and so a future translation change can be
-- replayed without an edge re-report. content_hash is the per-profile canonical
-- sha256 computed by the platform (never taken from the wire); an unchanged
-- re-report after hello only refreshes last_seen_at.
--
-- adopted_profile_id links the cached edge profile to the native device
-- profile created/updated by an explicit adopt action. It is a soft link so
-- deleting the native profile leaves the cache intact for re-adoption.

CREATE TABLE IF NOT EXISTS gateway_edge_profile (
    tenant_id            UUID NOT NULL REFERENCES tenant (id) ON DELETE CASCADE,
    gateway_id           UUID NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    content_hash         TEXT NOT NULL,
    payload              JSONB NOT NULL,
    resource_count       INTEGER NOT NULL DEFAULT 0,
    first_seen_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    adopted_profile_id   UUID REFERENCES device_profile (id) ON DELETE SET NULL,
    adopted_at           TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, gateway_id, name)
);

-- Tenant-wide listing / duplicate-name lookup during adopt.
CREATE INDEX IF NOT EXISTS gateway_edge_profile_tenant_idx
    ON gateway_edge_profile (tenant_id, name);
