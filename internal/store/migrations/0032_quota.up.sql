-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0032 introduces per-tenant soft quotas (ADR-0014). Unlike the license
-- device cap (a hard wall), quotas are operational limits the platform
-- administrator sets and the tenant administrator may later lower. The
-- defaults match the factory values: 10,000 devices, 1,000 ingest
-- messages/second and 100 WebSocket subscriptions; zero means unlimited.
-- The ingest rate is stored now and enforced by the cross-node KV limiter
-- added later.

CREATE TABLE tenant_quota (
    tenant_id UUID PRIMARY KEY REFERENCES tenant(id) ON DELETE CASCADE,
    device_limit BIGINT NOT NULL DEFAULT 10000,
    ingest_rate_limit BIGINT NOT NULL DEFAULT 1000,
    ws_subscription_limit BIGINT NOT NULL DEFAULT 100,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT tenant_quota_device_chk CHECK (device_limit >= 0),
    CONSTRAINT tenant_quota_ingest_chk CHECK (ingest_rate_limit >= 0),
    CONSTRAINT tenant_quota_ws_chk CHECK (ws_subscription_limit >= 0)
);
