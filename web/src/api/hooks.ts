// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// hooks.ts exposes pre-storage hook management and telemetry DLQ calls.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { Page } from "./devices";


// hookBase is the storage hook REST prefix.
const hookBase = () => `/v1/tenants/${currentTenantId()}/storage-hooks`;

// dlqBase is the telemetry DLQ REST prefix.
const dlqBase = () => `/v1/tenants/${currentTenantId()}/telemetry-dlq`;

// StorageHook is one pre-storage hook row.
export interface StorageHook {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
  hook_type: string;
  measurement_keys: string[];
  config: {
    script?: string;
    url?: string;
    module_b64?: string;
    module_size?: number;
    module_sha256?: string;
  };
  timeout_ms: number;
  failure_policy: string;
  created_at: string;
  updated_at: string;
}

// DLQRecord is one dead-letter row.
export interface DLQRecord {
  id: number;
  time: string;
  tenant_id: string;
  device_id: string;
  measurement_key: string;
  point: Record<string, unknown>;
  hook_name: string;
  reason: string;
}

// listStorageHooks fetches all hooks of the tenant.
export async function listStorageHooks(): Promise<StorageHook[]> {
  const { data } = await client.get<StorageHook[]>(hookBase());
  return data;
}

// createStorageHook creates one hook.
export async function createStorageHook(
  input: Omit<StorageHook, "id" | "created_at" | "updated_at">,
): Promise<StorageHook> {
  const { data } = await client.post<StorageHook>(hookBase(), input);
  return data;
}

// updateStorageHook updates one hook.
export async function updateStorageHook(
  id: string,
  input: Omit<StorageHook, "id" | "created_at" | "updated_at">,
): Promise<StorageHook> {
  const { data } = await client.put<StorageHook>(`${hookBase()}/${id}`, input);
  return data;
}

// deleteStorageHook deletes one hook.
export async function deleteStorageHook(id: string): Promise<void> {
  await client.delete(`${hookBase()}/${id}`);
}

// DLQFilter narrows a DLQ listing.
export interface DLQFilter {
  // hookName matches the failing hook exactly when set.
  hookName?: string;
  // device matches device_id as a case-insensitive substring.
  device?: string;
}

// listDLQ fetches one page of DLQ rows.
export async function listDLQ(
  page: number,
  pageSize: number,
  filter: DLQFilter = {},
): Promise<Page<DLQRecord>> {
  const { data } = await client.get<Page<DLQRecord>>(dlqBase(), {
    params: {
      page,
      page_size: pageSize,
      hook_name: filter.hookName || undefined,
      device: filter.device || undefined,
    },
  });
  return data;
}

// deleteDLQ removes the supplied DLQ rows.
export async function deleteDLQ(ids: number[]): Promise<void> {
  await client.post(`${dlqBase()}/delete`, { ids });
}

// replayDLQ re-inserts the supplied points into telemetry.
export async function replayDLQ(ids: number[]): Promise<number> {
  const { data } = await client.post<{ replayed: number }>(
    `${dlqBase()}/replay`,
    { ids },
  );
  return data.replayed;
}
