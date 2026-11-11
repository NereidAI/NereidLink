// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// companions.ts exposes the platform-neutral companion connector
// (companion-subscription/v1) administration surface: endpoint registration
// and the desired/read-back subscription rows. The bearer token is
// write-only; the API reports only auth_token_set, never the secret itself.

import client from "./client";
import { currentTenantId } from "./tenant";

// CompanionEndpoint is one registered companion installation.
export interface CompanionEndpoint {
  id: string;
  companion_id: string;
  gateway_id: string;
  base_url: string;
  contract_version: string;
  auth_token_set: boolean;
  insecure_skip_verify: boolean;
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

// CompanionEndpointInput is the create/update payload. On update, omitting
// auth_token preserves the stored secret; an empty string clears it.
export interface CompanionEndpointInput {
  companion_id: string;
  gateway_id: string;
  base_url: string;
  contract_version?: string;
  auth_token?: string;
  insecure_skip_verify?: boolean;
  enabled?: boolean;
}

// CompanionDesiredRow is one desired subscription with read-back status.
export interface CompanionDesiredRow {
  id: string;
  source_device: string;
  camera: string;
  pipeline_id: string;
  conclusion_device_id: string;
  enabled: boolean;
  artifact_key: string;
  revision: number;
  status: "PENDING" | "APPLIED" | "FAILED";
  reason: string;
}

// base returns the tenant companion-endpoint route prefix.
function base(): string {
  return `/v1/tenants/${currentTenantId()}/companion-endpoints`;
}

// listCompanionEndpoints fetches every endpoint for the current tenant.
export async function listCompanionEndpoints(): Promise<CompanionEndpoint[]> {
  const { data } = await client.get<CompanionEndpoint[]>(base());
  return data;
}

// createCompanionEndpoint registers one endpoint.
export async function createCompanionEndpoint(
  input: CompanionEndpointInput,
): Promise<CompanionEndpoint> {
  const { data } = await client.post<CompanionEndpoint>(base(), input);
  return data;
}

// updateCompanionEndpoint updates one endpoint by surrogate id.
export async function updateCompanionEndpoint(
  id: string,
  input: CompanionEndpointInput,
): Promise<CompanionEndpoint> {
  const { data } = await client.put<CompanionEndpoint>(`${base()}/${id}`, input);
  return data;
}

// deleteCompanionEndpoint removes one endpoint and cascades its desired rows.
export async function deleteCompanionEndpoint(id: string): Promise<void> {
  await client.delete(`${base()}/${id}`);
}

// listCompanionSubscriptions fetches desired rows with read-back status.
export async function listCompanionSubscriptions(
  endpointId: string,
): Promise<CompanionDesiredRow[]> {
  const { data } = await client.get<{ subscriptions: CompanionDesiredRow[] }>(
    `${base()}/${endpointId}/subscriptions`,
  );
  return data.subscriptions;
}
