// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secretTypes.ts exposes the edge secret type descriptor catalog
// (ADR-0034). A descriptor declares the fields a credential delivered to a
// gateway carries, as a JSON Schema; the console renders forms from it.
// Edge-advertised descriptors are read-only, tenant-defined descriptors are
// full CRUD, and an edge descriptor can be cloned into an editable one.

import client from "./client";
import { currentTenantId } from "./tenant";

// DescriptorSource marks how a row was created.
export type DescriptorSource = "edge" | "manual" | "blank";

// SecretType is one descriptor row.
export interface SecretType {
  id: string;
  tenant_id: string;
  secret_type: string;
  type_version: string;
  fields_schema: Record<string, unknown>;
  display_name?: Record<string, string>;
  source: DescriptorSource;
  registered_by?: string;
  created_at: string;
  updated_at: string;
}

// SecretTypeInput is the create/update payload.
export interface SecretTypeInput {
  type: string;
  type_version?: string;
  fields_schema: Record<string, unknown>;
  display_name?: Record<string, string>;
}

// base is the tenant-scoped route prefix.
function base(): string {
  return `/v1/tenants/${currentTenantId()}/edge-secret-types`;
}

// versionSuffix appends the optional version query parameter.
function versionSuffix(version: string): string {
  return version ? `?version=${encodeURIComponent(version)}` : "";
}

// listSecretTypes fetches all descriptors for the current tenant.
export async function listSecretTypes(): Promise<SecretType[]> {
  const { data } = await client.get<{ items: SecretType[] }>(base());
  return data.items;
}

// getSecretType fetches one descriptor by type and optional version.
export async function getSecretType(
  secretType: string,
  version = "",
): Promise<SecretType> {
  const { data } = await client.get<SecretType>(
    `${base()}/${encodeURIComponent(secretType)}${versionSuffix(version)}`,
  );
  return data;
}

// createSecretType adds a tenant-defined descriptor.
export async function createSecretType(input: SecretTypeInput): Promise<SecretType> {
  const { data } = await client.post<SecretType>(base(), input);
  return data;
}

// updateSecretType replaces one tenant-defined descriptor.
export async function updateSecretType(
  secretType: string,
  version: string,
  input: SecretTypeInput,
): Promise<SecretType> {
  const { data } = await client.put<SecretType>(
    `${base()}/${encodeURIComponent(secretType)}${versionSuffix(version)}`,
    input,
  );
  return data;
}

// deleteSecretType removes one tenant-defined descriptor.
export async function deleteSecretType(
  secretType: string,
  version = "",
): Promise<void> {
  await client.delete(
    `${base()}/${encodeURIComponent(secretType)}${versionSuffix(version)}`,
  );
}

// cloneSecretType copies an edge descriptor into a new manual one.
export async function cloneSecretType(
  secretType: string,
  version: string,
  newType: string,
): Promise<SecretType> {
  const { data } = await client.post<SecretType>(
    `${base()}/${encodeURIComponent(secretType)}${versionSuffix(version)}`,
    { new_type: newType },
  );
  return data;
}
