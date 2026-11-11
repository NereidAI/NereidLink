// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ota.ts exposes firmware rollout task management (ADR-0006b/ADR-0026):
// create a hosted (firmware_id) or external-URL task, list tasks, read one
// aggregate and page its per-device items. Live progress arrives over the
// ota_task WebSocket resource rather than polling.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { Page } from "./devices";

// OtaSuccessPolicy selects how a device reaches the succeeded terminal state.
export type OtaSuccessPolicy =
  | "device_reported"
  | "confirmed_after_reboot";

// OtaTargetMode tells whether targets were an explicit list or a dynamic
// selector snapshot (ADR-0006d §1).
export type OtaTargetMode = "explicit" | "selector";

// OtaTargetSelector chooses a fleet by profile/group/customer; set fields are
// AND-combined.
export interface OtaTargetSelector {
  profile_id?: string;
  group_id?: string;
  customer_id?: string;
}

// OtaRolloutPolicy is the per-task canary/abort/watchdog configuration. Null
// fields inherit the server default; the legacy single-batch behaviour uses
// both batch fields unset.
export interface OtaRolloutPolicy {
  batch_percent?: number | null;
  batch_size?: number | null;
  batch_pause_seconds?: number;
  failure_abort_percent?: number | null;
  max_consecutive_failures?: number | null;
  stall_timeout_seconds?: number | null;
  confirm_timeout_seconds?: number | null;
}

// OtaTask is one rollout and its aggregate progress.
export interface OtaTask {
  id: string;
  tenant_id?: string;
  created_by?: string;
  firmware_id?: string;
  firmware_version: string;
  package_url?: string;
  package_sha256?: string;
  package_size?: number;
  state:
    | "pending"
    | "running"
    | "paused"
    | "completed"
    | "partial"
    | "failed"
    | "aborted"
    | "canceled";
  success_policy: OtaSuccessPolicy;
  target_mode: OtaTargetMode;
  target_selector?: OtaTargetSelector;
  policy: OtaRolloutPolicy;
  batch_no: number;
  next_batch_at?: string;
  abort_reason?: string;
  total_count: number;
  succeeded_count: number;
  failed_count: number;
  canceled_count: number;
  skipped_count: number;
  error?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

// OtaItemState is the per-device ordered phase.
export type OtaItemState =
  | "pending"
  | "initiated"
  | "downloading"
  | "downloaded"
  | "verified"
  | "updating"
  | "updated"
  | "rebooting"
  | "succeeded"
  | "failed"
  | "canceled"
  | "skipped";

// OtaResultCode is the structured failure/reason axis.
export type OtaResultCode =
  | "none"
  | "flash_full"
  | "out_of_ram"
  | "connection_lost"
  | "integrity_check_failed"
  | "unsupported_type"
  | "model_mismatch"
  | "version_mismatch"
  | "invalid_uri"
  | "apply_failed"
  | "reboot_timeout"
  | "download_no_progress"
  | "already_current"
  | "canceled"
  | "unknown";

// OtaItem is the per-device member of a task.
export interface OtaItem {
  id: string;
  task_id: string;
  tenant_id?: string;
  gateway_id: string;
  device_id: string;
  state: OtaItemState;
  result_code?: OtaResultCode;
  progress_pct: number;
  bytes_received?: number;
  // batch_no is the opened batch (0 = legacy single batch). Negative numbers
  // never appear to clients: deferred batches are not returned as pending.
  batch_no?: number;
  message?: string;
  created_at: string;
  updated_at: string;
  finished_at?: string;
}

// OtaTimelineEvent is one append-only row of a device's phase history.
export interface OtaTimelineEvent {
  id: string;
  task_id: string;
  tenant_id?: string;
  device_id: string;
  gateway_id?: string;
  seq: number;
  phase: OtaItemState | "confirmed";
  result_code?: OtaResultCode;
  progress_pct: number;
  bytes_received?: number;
  message?: string;
  source: "platform" | "device" | "gateway";
  created_at: string;
}

// CreateOtaTaskInput starts a rollout. When firmware_id is provided the task
// is platform-hosted and the package descriptor is ignored; otherwise the
// external package fields are required. Provide either devices (explicit
// snapshot) or selector (dynamic fleet), never both.
export interface CreateOtaTaskInput {
  devices?: string[];
  selector?: OtaTargetSelector;
  firmware_id?: string;
  success_policy?: OtaSuccessPolicy;
  policy?: OtaRolloutPolicy;
  package?: {
    version: string;
    url: string;
    sha256: string;
    size: number;
    headers?: Record<string, string>;
  };
}

// OtaListParams narrows a paged task listing.
export interface OtaListParams {
  state?: string;
  page?: number;
  page_size?: number;
}

// base is the OTA task REST prefix.
const base = () => `/v1/tenants/${currentTenantId()}/ota-tasks`;

// createOtaTask starts an asynchronous rollout (202).
export async function createOtaTask(
  input: CreateOtaTaskInput,
): Promise<OtaTask> {
  const { data } = await client.post<OtaTask>(base(), input);
  return data;
}

// listOtaTasks returns a paged task list.
export async function listOtaTasks(
  params: OtaListParams,
): Promise<Page<OtaTask>> {
  const { data } = await client.get<Page<OtaTask>>(base(), { params });
  return data;
}

// getOtaTask returns one rollout aggregate.
export async function getOtaTask(id: string): Promise<OtaTask> {
  const { data } = await client.get<OtaTask>(`${base()}/${id}`);
  return data;
}

// DeviceOtaItem is one rollout row from a single device's point of view:
// the per-device item joined to its parent task's target version/state.
export interface DeviceOtaItem extends OtaItem {
  firmware_version: string;
  task_state: OtaTask["state"];
}

// listDeviceOtaItems returns a paged, newest-first list of rollout rows for
// one device, each joined to the parent task.
export async function listDeviceOtaItems(
  deviceId: string,
  params: { page?: number; page_size?: number } = {},
): Promise<Page<DeviceOtaItem>> {
  const { data } = await client.get<Page<DeviceOtaItem>>(
    `/v1/tenants/${currentTenantId()}/devices/${deviceId}/ota-items`,
    { params },
  );
  return data;
}

// listOtaItems returns a paged list of per-device task members.
export async function listOtaItems(
  id: string,
  params: { page?: number; page_size?: number } = {},
): Promise<Page<OtaItem>> {
  const { data } = await client.get<Page<OtaItem>>(
    `${base()}/${id}/items`,
    { params },
  );
  return data;
}

// listOtaEvents returns the append-only per-device phase timeline of a task,
// optionally narrowed to one device.
export async function listOtaEvents(
  id: string,
  deviceId?: string,
): Promise<OtaTimelineEvent[]> {
  const { data } = await client.get<{ events: OtaTimelineEvent[] }>(
    `${base()}/${id}/events`,
    { params: deviceId ? { device_id: deviceId } : {} },
  );
  return data.events;
}

// cancelOtaTask applies the safe best-effort cancel. Devices that had already
// started applying are not interrupted; the response reports their count.
export async function cancelOtaTask(
  id: string,
): Promise<{ task: OtaTask; still_applying_count: number }> {
  const { data } = await client.post<{
    task: OtaTask;
    still_applying_count: number;
  }>(`${base()}/${id}/cancel`);
  return data;
}

// pauseOtaTask holds a running rollout: no new batches open, but devices
// already applying are not interrupted (ADR-0006d §5).
export async function pauseOtaTask(id: string): Promise<OtaTask> {
  const { data } = await client.post<OtaTask>(`${base()}/${id}/pause`);
  return data;
}

// resumeOtaTask releases a held rollout and dispatches its open batch.
export async function resumeOtaTask(id: string): Promise<OtaTask> {
  const { data } = await client.post<OtaTask>(`${base()}/${id}/resume`);
  return data;
}
