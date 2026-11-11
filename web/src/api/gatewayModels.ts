// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gatewayModels.ts reads a gateway's edge model inventory (ADR-0039),
// reported in the full-replacement manifest models segment. Read-only; the
// binding wizard uses it to offer only READY, non-stale models.

import client from "./client";
import { currentTenantId } from "./tenant";

// Model states reported by the edge (manifest models segment).
export type ModelState =
  | "EXTRACTING"
  | "VALIDATING"
  | "READY"
  | "FAILED"
  | "REMOVED";

// GatewayModel is one model present on one gateway.
export interface GatewayModel {
  id: string;
  version: string;
  revision?: string;
  task: string;
  family?: string;
  backend: string;
  // Concrete model-server product (e.g. ovms/triton/external) reported by
  // newer edge firmware; absent on older builds. Distinct from backend,
  // which is the wire protocol family. The UI falls back to backend.
  server_family?: string;
  model_name?: string;
  state: ModelState;
  reason?: string;
  labels: string[];
  active_bindings: number;
  stale: boolean;
  stale_at?: string;
  first_seen: string;
  updated_at: string;
}

// listGatewayModels fetches the gateway's reported model inventory.
export async function listGatewayModels(
  gatewayId: string,
): Promise<GatewayModel[]> {
  const { data } = await client.get<GatewayModel[]>(
    `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}/models`,
  );
  return data;
}

// isReadyForBinding reports whether a model may back a new edge-inference
// connector: READY and not stale (its gw-manifest lease is alive).
export function isReadyForBinding(model: GatewayModel): boolean {
  return model.state === "READY" && !model.stale;
}
