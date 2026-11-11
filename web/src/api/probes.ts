// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// probes.ts is the typed client for on-demand gateway probe tasks
// (ADR-0026 section 9). A probe asks a gateway to check the reachability
// of a set of its bound sub-devices; the platform only relays mode and
// timeout and never interprets the per-device result payload.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { Page } from "./devices";
import { newUUID } from "../utils/id";

// ProbeTask is an on-demand probe and its aggregate progress.
export interface ProbeTask {
  id: string;
  tenant_id: string;
  created_by?: string;
  mode: string;
  timeout_ms: number;
  state: "pending" | "running" | "completed" | "partial" | "failed";
  total_count: number;
  succeeded_count: number;
  failed_count: number;
  error?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

// ProbeTaskItem is one per-device probe outcome. The backend models
// Result as []byte, so it is delivered as a base64 string holding the
// opaque result JSON the gateway returned.
export interface ProbeTaskItem {
  id: string;
  task_id: string;
  tenant_id: string;
  gateway_id: string;
  device_id: string;
  state: "pending" | "running" | "succeeded" | "failed";
  result?: string;
  error?: string;
  created_at: string;
  finished_at?: string;
}

// CreateProbeRequest starts an on-demand probe. Mode empty defaults to
// fresh server-side; timeout_ms 0 uses the service default (10000).
export interface CreateProbeRequest {
  devices: string[];
  mode?: "cached" | "fresh" | "strict";
  timeout_ms?: number;
}

// decodeProbeResult decodes the base64 result of one item into the
// opaque JSON value the edge reported (for example
// {"reachable":true,"rtt_ms":12}). It returns undefined when the item
// carries no result or the payload cannot be decoded.
export function decodeProbeResult(item: ProbeTaskItem): unknown {
  if (!item.result) return undefined;
  try {
    const binary = atob(item.result);
    return JSON.parse(binary) as unknown;
  } catch {
    return undefined;
  }
}

// newUUID is re-exported for existing call sites; the helper lives in utils/id.
export { newUUID };

// createProbe starts an asynchronous probe task and returns its initial
// state. A fresh Idempotency-Key is attached so a retried request with
// the same body replays the original 202 instead of creating a second
// task.
export async function createProbe(request: CreateProbeRequest): Promise<ProbeTask> {
  const { data } = await client.post<ProbeTask>(
    `/v1/tenants/${currentTenantId()}/probes`,
    request,
    { headers: { "Idempotency-Key": newUUID() } },
  );
  return data;
}

// ListProbesParams narrows a probe task list; all fields optional.
export interface ListProbesParams {
  state?: string;
  mode?: string;
  q?: string;
}

// listProbes returns a page of probe tasks, newest first, applying the
// optional state/mode/name filter.
export async function listProbes(
  params: ListProbesParams = {},
  page = 1,
  pageSize = 10,
): Promise<Page<ProbeTask>> {
  const { data } = await client.get<Page<ProbeTask>>(
    `/v1/tenants/${currentTenantId()}/probes`,
    { params: { ...params, page, page_size: pageSize } },
  );
  return data;
}

// getProbe returns one probe task.
export async function getProbe(taskId: string): Promise<ProbeTask> {
  const { data } = await client.get<ProbeTask>(
    `/v1/tenants/${currentTenantId()}/probes/${taskId}`,
  );
  return data;
}

// listProbeItems returns per-device probe outcomes for one task.
export async function listProbeItems(
  taskId: string,
  page = 1,
  pageSize = 100,
): Promise<Page<ProbeTaskItem>> {
  const { data } = await client.get<Page<ProbeTaskItem>>(
    `/v1/tenants/${currentTenantId()}/probes/${taskId}/items`,
    { params: { page, page_size: pageSize } },
  );
  return data;
}
