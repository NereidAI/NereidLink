// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// firmware.ts exposes the platform-hosted firmware repository (ADR-0006b):
// package metadata listing, streamed multipart upload, stable marking,
// deletion and an operator presigned download URL. Device downloads do not
// use these calls; they go through the authenticated /v1/me relay.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { Page } from "./devices";

// FirmwarePackage is one stored artifact's metadata.
export interface FirmwarePackage {
  id: string;
  tenant_id?: string;
  profile_id?: string;
  // name is the human-readable release identity (required); version is the
  // technical version used for already_current comparisons.
  name: string;
  version: string;
  release_notes?: string;
  filename: string;
  content_type?: string;
  size_bytes: number;
  sha256: string;
  is_stable: boolean;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

// FirmwareListParams narrows a paged repository listing.
export interface FirmwareListParams {
  search?: string;
  profile_id?: string;
  page?: number;
  page_size?: number;
}

// UploadFirmwareInput carries the artifact and its metadata. Metadata fields
// are appended before the file so the server can stream the upload.
export interface UploadFirmwareInput {
  file: File;
  // name is the required human-readable release title (ADR-0006d §2).
  name: string;
  version: string;
  release_notes?: string;
  filename?: string;
  profile_id?: string;
  content_type?: string;
  is_stable?: boolean;
}

// PresignedDownload is the operator browser download descriptor.
export interface PresignedDownload {
  url: string;
  filename: string;
  version: string;
}

// base is the firmware repository REST prefix.
const base = () => `/v1/tenants/${currentTenantId()}/firmware`;

// listFirmware returns a filtered, paged package list.
export async function listFirmware(
  params: FirmwareListParams,
): Promise<Page<FirmwarePackage>> {
  const { data } = await client.get<Page<FirmwarePackage>>(base(), { params });
  return data;
}

// uploadFirmware streams one artifact to object storage via multipart.
export async function uploadFirmware(
  input: UploadFirmwareInput,
): Promise<FirmwarePackage> {
  const form = new FormData();
  form.append("name", input.name);
  form.append("version", input.version);
  if (input.release_notes) form.append("release_notes", input.release_notes);
  if (input.filename) form.append("filename", input.filename);
  if (input.profile_id) form.append("profile_id", input.profile_id);
  if (input.content_type) form.append("content_type", input.content_type);
  form.append("is_stable", input.is_stable ? "true" : "false");
  form.append("file", input.file);
  const { data } = await client.post<FirmwarePackage>(base(), form);
  return data;
}

// setStable marks or unmarks one package as a recommended rollback version.
export async function setFirmwareStable(
  id: string,
  stable: boolean,
): Promise<FirmwarePackage> {
  const { data } = await client.post<FirmwarePackage>(
    `${base()}/${id}/stable`,
    null,
    { params: { stable: stable ? "true" : "false" } },
  );
  return data;
}

// deleteFirmware removes one package's metadata and backing object.
export async function deleteFirmware(id: string): Promise<void> {
  await client.delete(`${base()}/${id}`);
}

// presignFirmware returns a short-lived operator URL for browser download.
export async function presignFirmware(
  id: string,
): Promise<PresignedDownload> {
  const { data } = await client.get<PresignedDownload>(
    `${base()}/${id}/download`,
  );
  return data;
}
