// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// apiKeys.ts is the typed client for self-service user API-key management.
// The plaintext key is returned only during creation or rotation.

import client from "./client";

export interface APIKey {
  id: string;
  tenant_id: string;
  user_id: string;
  name: string;
  key_prefix: string;
  scopes: string[];
  expires_at?: string | null;
  revoked: boolean;
  last_used_at?: string | null;
  created_at: string;
  created_by: string;
}

export interface CreateAPIKeyRequest {
  name: string;
  scopes?: string[];
  expires_at?: string;
}

export interface CreatedAPIKey {
  item: APIKey;
  api_key: string;
}

export interface RotatedAPIKey {
  item: APIKey;
  api_key: string;
  old_prefix: string;
}

interface APIKeyList {
  items: APIKey[];
}

export async function listAPIKeys(): Promise<APIKey[]> {
  const { data } = await client.get<APIKeyList>("/v1/auth/api-keys");
  return data.items ?? [];
}

export async function createAPIKey(
  input: CreateAPIKeyRequest,
): Promise<CreatedAPIKey> {
  const { data } = await client.post<CreatedAPIKey>("/v1/auth/api-keys", input);
  return data;
}

export async function rotateAPIKey(
  id: string,
  expiresAt?: string,
): Promise<RotatedAPIKey> {
  const { data } = await client.post<RotatedAPIKey>(
    `/v1/auth/api-keys/${id}/rotate`,
    { expires_at: expiresAt },
  );
  return data;
}

export async function revokeAPIKey(id: string): Promise<APIKey> {
  const { data } = await client.post<APIKey>(
    `/v1/auth/api-keys/${id}/revoke`,
  );
  return data;
}

export async function deleteAPIKey(id: string): Promise<void> {
  await client.delete(`/v1/auth/api-keys/${id}`);
}

// Tenant administrators use these cross-user routes. Creation remains a
// self-service action because every key is owned by its creating user.
export async function listTenantAPIKeys(
  tenantId: string,
): Promise<APIKey[]> {
  const { data } = await client.get<APIKeyList>(
    `/v1/tenants/${tenantId}/api-keys`,
  );
  return data.items ?? [];
}

export async function revokeTenantAPIKey(
  tenantId: string,
  id: string,
): Promise<APIKey> {
  const { data } = await client.post<APIKey>(
    `/v1/tenants/${tenantId}/api-keys/${id}/revoke`,
  );
  return data;
}

export async function deleteTenantAPIKey(
  tenantId: string,
  id: string,
): Promise<void> {
  await client.delete(`/v1/tenants/${tenantId}/api-keys/${id}`);
}
