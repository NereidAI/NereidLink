// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gatewayOperations.ts exposes the gateway operation catalog
// (ADR-0035): effective catalog, private descriptor CRUD/clone and
// disable/enable.

import client from "./client";
import { currentTenantId } from "./tenant";

// Advertiser identifies one gateway that advertises an aggregate operation.
export interface OperationAdvertiser {
  id: string;
  name: string;
}

// OperationView is one effective catalog row.
export interface OperationView {
  operation: string;
  type_version: string;
  interaction: string;
  timeout_ms: number;
  param_schema: Record<string, unknown>;
  result_schema: Record<string, unknown>;
  risk: string;
  display_name?: Record<string, string>;
  description?: Record<string, string>;
  source: string;
  disabled: boolean;
  advertised_by?: OperationAdvertiser[];
  gateway_count?: number;
  conflict?: boolean;
}

// OperationInput is a private descriptor payload.
export interface OperationInput {
  operation: string;
  type_version?: string;
  interaction?: string;
  timeout_ms?: number;
  risk?: string;
  param_schema?: Record<string, unknown>;
  result_schema?: Record<string, unknown>;
  display_name?: Record<string, string>;
  description?: Record<string, string>;
}

// base is the tenant-scoped route prefix.
function base(): string {
  return `/v1/tenants/${currentTenantId()}/gateway-operations`;
}

// versionSuffix appends the version query parameter.
function versionSuffix(version: string): string {
  return version ? `?version=${encodeURIComponent(version)}` : "";
}

// listOperations fetches the tenant aggregate catalog.
export async function listOperations(): Promise<OperationView[]> {
  const { data } = await client.get<{ items: OperationView[] }>(base());
  return data.items;
}

// listGatewayOperations fetches one gateway's own catalog: what it advertised
// plus tenant-private fallback descriptors.
export async function listGatewayOperations(
  gatewayId: string,
): Promise<OperationView[]> {
  const { data } = await client.get<{ items: OperationView[] }>(
    `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}/operations`,
  );
  return data.items;
}

// createOperation adds a private descriptor.
export async function createOperation(input: OperationInput): Promise<void> {
  await client.post(base(), input);
}

// updateOperation replaces one private descriptor.
export async function updateOperation(
  operation: string,
  version: string,
  input: OperationInput,
): Promise<void> {
  await client.put(
    `${base()}/${encodeURIComponent(operation)}${versionSuffix(version)}`,
    input,
  );
}

// deleteOperation removes one private descriptor.
export async function deleteOperation(
  operation: string,
  version: string,
): Promise<void> {
  await client.delete(
    `${base()}/${encodeURIComponent(operation)}${versionSuffix(version)}`,
  );
}

// cloneOperation clones an edge descriptor into a private one.
export async function cloneOperation(
  operation: string,
  version: string,
  newKey: string,
): Promise<void> {
  await client.post(
    `${base()}/${encodeURIComponent(operation)}${versionSuffix(version)}`,
    { type_version: version, new_type: newKey },
  );
}

// setOperationDisabled toggles the disable mark.
export async function setOperationDisabled(
  operation: string,
  version: string,
  disabled: boolean,
): Promise<void> {
  await client.put(`${base()}/${encodeURIComponent(operation)}/disabled`, {
    type_version: version,
    disabled,
  });
}
