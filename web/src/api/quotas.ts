// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// quotas.ts exposes per-tenant soft quota reads and updates (ADR-0014).

import client from "./client";

// TenantQuota is the effective quota of a tenant.
export interface TenantQuota {
  tenant_id: string;
  device_limit: number;
  ingest_rate_limit: number;
  ws_subscription_limit: number;
  updated_at?: string;
}

// QuotaWrite carries optional limit overrides.
export interface QuotaWrite {
  device_limit?: number;
  ingest_rate_limit?: number;
  ws_subscription_limit?: number;
}

// getTenantQuota fetches the effective quota of a tenant.
export async function getTenantQuota(tenantID: string): Promise<TenantQuota> {
  const { data } = await client.get<TenantQuota>(
    `/v1/tenants/${tenantID}/quota`,
  );
  return data;
}

// updateTenantQuota upserts the quota of a tenant. Platform admin only.
export async function updateTenantQuota(
  tenantID: string,
  input: QuotaWrite,
): Promise<TenantQuota> {
  const { data } = await client.put<TenantQuota>(
    `/v1/tenants/${tenantID}/quota`,
    input,
  );
  return data;
}
