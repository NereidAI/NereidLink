// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profiles.ts exposes device profile management calls with their embedded
// measurement descriptors.

import client from "./client";
import { currentTenantId } from "./tenant";
import { multipartUpload, type ImportPreview } from "./imports";


// Breakpoint is one table-conversion point.
export interface Breakpoint {
  x: number;
  y: number;
}

// Descriptor is one measurement descriptor.
export interface Descriptor {
  id?: string;
  measurement_key: string;
  unit: string;
  transform_type: string;
  scale: number;
  offset: number;
  breakpoints: Breakpoint[];
  enum_map: Record<string, string>;
  expression: string;
  precision: number;
  // ADR-0014 online-but-silent monitoring. Positive expected period in
  // seconds enables detection; null/undefined leaves the point unmonitored.
  expected_period_seconds?: number | null;
  // Silence deadline multiplier; undefined uses the server default of 3.
  tolerance_multiplier?: number;
  version?: number;
  // ADR-0032 neutral spec (edge/import-owned) fields.
  data_type?: NeutralType;
  read_write?: string;
  address?: Record<string, unknown>;
  edge_default?: string;
  edge_min?: number | null;
  edge_max?: number | null;
  inactive?: boolean;
}

// NeutralType is the vendor-neutral point datatype enum.
export type NeutralType = "bool" | "int" | "uint" | "float" | "string" | "binary";

// ProfileSource records where library profile content came from.
export type ProfileSource = "edge" | "import" | "manual";

// ProfileAvailability marks whether a library profile is referenced.
export type ProfileAvailability = "discovered" | "active";

// Profile is one profile with embedded descriptors.
export interface Profile {
  id: string;
  name: string;
  rule_chain_group_id?: string;
  descriptors: Descriptor[];
  // connector_type is non-empty only on profiles that carry an ADR-0026
  // connector template; only such profiles may be bound to a connector.
  connector_type?: string;
  connector_type_version?: string;
  connector_config_template?: Record<string, unknown>;
  connector_mappings?: unknown[];
  // ADR-0032 profile-library metadata.
  source?: ProfileSource;
  protocol?: string;
  content_hash?: string;
  availability?: ProfileAvailability;
}

// ProfileInput is the create/update payload. Descriptors are embedded.
export interface ProfileInput {
  name: string;
  rule_chain_group_id?: string;
  descriptors: Omit<Descriptor, "id" | "version">[];
  connector_type?: string;
  connector_type_version?: string;
  connector_config_template?: Record<string, unknown>;
  connector_mappings?: unknown[];
  protocol?: string;
}

// base is the profile REST prefix.
const base = () => `/v1/tenants/${currentTenantId()}/profiles`;

// listProfiles fetches all profiles with embedded descriptors.
export async function listProfiles(): Promise<Profile[]> {
  const { data } = await client.get<Profile[]>(base());
  return data;
}

// createProfile creates one profile.
export async function createProfile(input: ProfileInput): Promise<Profile> {
  const { data } = await client.post<Profile>(base(), input);
  return data;
}

// updateProfile replaces one profile and its descriptor set.
export async function updateProfile(
  id: string,
  input: ProfileInput,
): Promise<Profile> {
  const { data } = await client.put<Profile>(`${base()}/${id}`, input);
  return data;
}

// deleteProfile deletes one profile.
export async function deleteProfile(id: string): Promise<void> {
  await client.delete(`${base()}/${id}`);
}

// BulkImportProfilesResult summarises a profile upload.
export interface BulkImportProfilesResult {
  total: number;
  created: number;
  failed: { line: number; name?: string; reason: string }[];
}

// bulkImportProfiles creates profiles from an uploaded CSV file.
export async function bulkImportProfiles(
  file: File,
): Promise<BulkImportProfilesResult> {
  return multipartUpload<BulkImportProfilesResult>(`${base()}/bulk-import`, file);
}

// previewProfileImport validates a profile CSV upload without writing.
export async function previewProfileImport(file: File): Promise<ImportPreview> {
  return multipartUpload<ImportPreview>(`${base()}/bulk-import/preview`, file);
}

// profileImportTemplateUrl returns the profile CSV template download URL.
export function profileImportTemplateUrl(): string {
  return `${base()}/bulk-import/template`;
}

// ProfileDocumentImportResult is the parse/import outcome.
export interface ProfileDocumentImportResult {
  profile_id?: string;
  point_count: number;
  warnings: string[];
}

// previewProfileDocument parses a pasted neutral/EdgeX profile without saving.
export async function previewProfileDocument(
  document: unknown,
): Promise<ProfileDocumentImportResult> {
  const { data } = await client.post<ProfileDocumentImportResult>(
    `${base()}/import/preview`,
    { document },
  );
  return data;
}

// importProfileDocument parses and stores a pasted profile document.
export async function importProfileDocument(
  document: unknown,
): Promise<ProfileDocumentImportResult> {
  const { data } = await client.post<ProfileDocumentImportResult>(
    `${base()}/import`,
    { document },
  );
  return data;
}
