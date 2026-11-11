// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow.ts exposes typed device shadow API calls.

import client from "./client";
import { currentTenantId } from "./tenant";


// Shadow is the complete desired/reported device shadow.
export interface Shadow {
  device_id: string;
  desired: Record<string, unknown>;
  reported: Record<string, unknown>;
  delta: Record<string, unknown>;
  desired_version: number;
  reported_version: number;
  etag: string;
  in_sync: boolean;
  desired_at?: string;
  reported_at?: string;
  updated_at: string;
}

// getShadow fetches one device shadow.
export async function getShadow(deviceId: string): Promise<Shadow> {
  const { data } = await client.get<Shadow>(
    `/v1/tenants/${currentTenantId()}/devices/${deviceId}/shadow`,
  );
  return data;
}

// updateDesired merges desired state with optimistic ETag matching.
export async function updateDesired(
  deviceId: string,
  state: Record<string, unknown>,
  etag: string,
): Promise<Shadow> {
  const { data } = await client.put<Shadow>(
    `/v1/tenants/${currentTenantId()}/devices/${deviceId}/shadow/desired`,
    { state },
    { headers: { "If-Match": etag } },
  );
  return data;
}

// DesiredKeyField is one profile-derived writable key for the editor.
export interface DesiredKeyField {
  key: string;
  type: "boolean" | "number" | "string";
  unit?: string;
  min?: number | null;
  max?: number | null;
  default?: unknown;
}

// DesiredSchema is the writable-key catalog derived from the device profile.
export interface DesiredSchema {
  device_id: string;
  profile_id?: string;
  fields: DesiredKeyField[];
}

// getDesiredSchema fetches the profile-derived catalog of writable desired
// keys. A device without a profile returns an empty field list.
export async function getDesiredSchema(deviceId: string): Promise<DesiredSchema> {
  const { data } = await client.get<DesiredSchema>(
    `/v1/tenants/${currentTenantId()}/devices/${deviceId}/shadow/desired-schema`,
  );
  return data;
}
