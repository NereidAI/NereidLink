// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// operationTasks.ts executes whole-gateway operations and reads task
// history (ADR-0035). Params may carry secret:// references; dangerous
// operations additionally require a verified step-up challenge id.

import client from "./client";
import { currentTenantId } from "./tenant";

// OperationTask is one persisted invocation.
export interface OperationTask {
  id: string;
  tenant_id: string;
  gateway_id: string;
  operation: string;
  type_version?: string;
  params: Record<string, unknown>;
  result?: unknown;
  status: string;
  reason?: string;
  risk: string;
  invoked_by: string;
  invoked_by_email?: string;
  started_at: string;
  finished_at?: string;
}

// gatewayPrefix builds the gateway-scoped route prefix.
function gatewayPrefix(gatewayId: string): string {
  return `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}`;
}

// invokeOperation executes one catalog operation synchronously. timeoutMs,
// when supplied, may only shorten the descriptor default and is clamped
// server-side into the 100ms..60s envelope.
export async function invokeOperation(
  gatewayId: string,
  operation: string,
  params: Record<string, unknown>,
  stepUpId?: string,
  timeoutMs?: number,
): Promise<OperationTask> {
  const { data } = await client.post<OperationTask>(
    `${gatewayPrefix(gatewayId)}/operations/${encodeURIComponent(operation)}/invoke`,
    { params, step_up_id: stepUpId ?? "", timeout_ms: timeoutMs ?? 0 },
  );
  return data;
}

// listOperationTasks reads the newest task history for one gateway.
export async function listOperationTasks(
  gatewayId: string,
): Promise<OperationTask[]> {
  const { data } = await client.get<OperationTask[]>(
    `${gatewayPrefix(gatewayId)}/operation-tasks`,
  );
  return data;
}
