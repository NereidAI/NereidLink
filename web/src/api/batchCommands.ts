// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// batchCommands.ts is the typed client for asynchronous batch command
// tasks and per-device results.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { Page } from "./devices";

// BatchTask is an asynchronous batch command.
export interface BatchTask {
  id: string;
  tenant_id: string;
  created_by?: string;
  name: string;
  params: Record<string, unknown>;
  timeout_ms: number;
  target_type: "group" | "devices";
  group_id?: string;
  state: "pending" | "running" | "completed" | "partial" | "failed";
  total_count: number;
  succeeded_count: number;
  failed_count: number;
  error?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}

// BatchTaskItem is one per-device task result.
export interface BatchTaskItem {
  id: string;
  task_id: string;
  tenant_id: string;
  device_id: string;
  command_id?: string;
  state: "pending" | "running" | "succeeded" | "failed";
  response?: Record<string, unknown>;
  error?: string;
  created_at: string;
  finished_at?: string;
}

// CreateBatchTaskRequest starts a batch command.
export interface CreateBatchTaskRequest {
  name: string;
  params?: Record<string, unknown>;
  timeout_ms?: number;
  group_id?: string;
  device_ids?: string[];
}

// createBatchCommand starts a task and returns its initial state.
export async function createBatchCommand(
  request: CreateBatchTaskRequest,
): Promise<BatchTask> {
  const { data } = await client.post<BatchTask>(
    `/v1/tenants/${currentTenantId()}/command-tasks`,
    request,
  );
  return data;
}

// listBatchCommands returns a page of tasks.
export async function listBatchCommands(
  page = 1,
  pageSize = 10,
): Promise<Page<BatchTask>> {
  const { data } = await client.get<Page<BatchTask>>(
    `/v1/tenants/${currentTenantId()}/command-tasks`,
    { params: { page, page_size: pageSize } },
  );
  return data;
}

// getBatchCommand returns one task.
export async function getBatchCommand(taskId: string): Promise<BatchTask> {
  const { data } = await client.get<BatchTask>(
    `/v1/tenants/${currentTenantId()}/command-tasks/${taskId}`,
  );
  return data;
}

// listBatchItems returns per-device task results.
export async function listBatchItems(
  taskId: string,
  page = 1,
  pageSize = 100,
): Promise<Page<BatchTaskItem>> {
  const { data } = await client.get<Page<BatchTaskItem>>(
    `/v1/tenants/${currentTenantId()}/command-tasks/${taskId}/items`,
    { params: { page, page_size: pageSize } },
  );
  return data;
}
