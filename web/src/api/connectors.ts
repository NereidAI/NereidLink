// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// connectors.ts exposes the ADR-0026 edge connector desired/reported
// lifecycle: per-gateway desired specs, the read-only reconcile view, and the
// edge-registered config-schema catalog.

import client from "./client";
import { currentTenantId } from "./tenant";

// BuiltinConnectorType is the NereidEdge connector that is hard-wired into
// every edge runtime. It is always offered in connector-type pickers even
// before any gateway has registered a config schema.
export const BUILTIN_CONNECTOR_TYPE = "nereid-edge";

// connectorTypeOptions merges edge-registered connector types with the
// built-in type, de-duplicated and sorted, for AutoComplete/Select pickers.
// Any value not listed here can still be typed in (the field is free-form).
export function connectorTypeOptions(
  schemas: Pick<ConnectorSchema, "connector_type">[],
): { value: string }[] {
  const types = new Set<string>([BUILTIN_CONNECTOR_TYPE]);
  for (const schema of schemas) {
    if (schema.connector_type) types.add(schema.connector_type);
  }
  return Array.from(types)
    .sort((a, b) => {
      if (a === BUILTIN_CONNECTOR_TYPE) return -1;
      if (b === BUILTIN_CONNECTOR_TYPE) return 1;
      return a.localeCompare(b);
    })
    .map((value) => ({ value }));
}


// ConnectorStatusStats preserves independent availability: zero is a reading,
// while an absent field is unavailable.
export interface ConnectorStatusStats {
  success_total?: number;
  error_total?: number;
  p50_ms?: number;
  p95_ms?: number;
}

// ConnectorSpec is one desired instance plus its last reported state.
export interface ConnectorSpec {
  spec_id: string;
  tenant_id: string;
  gateway_id: string;
  connector_type: string;
  type_version: string;
  config: Record<string, unknown>;
  mappings: unknown;
  revision: number;
  device_id?: string;
  profile_id?: string;
  state: string;
  reported_revision: number;
  report_reason?: string;
  not_applied_alarm: boolean;
  failed_alarm: boolean;
  failed_critical: boolean;
  failed_at?: string;
  created_at: string;
  updated_at: string;
  desired_at: string;
  dispatched_at?: string;
  reported_at?: string;
  stats?: ConnectorStatusStats;
  observed_revision?: number;
  observed_hash?: string;
  manifest_revision: number;
  reported_config_hash?: string;
  last_manifest_at?: string;
  health_check?: ConnectorHealthCheck;
}

// ConnectorHealthCheck is the per-instance liveness probe policy. An empty
// object (or undefined) disables probing.
export interface ConnectorHealthCheck {
  resource: string;
  interval_ms: number;
  timeout_ms: number;
  fail_threshold: number;
}

// ConnectorSaveInput is the desired half written from the console.
export interface ConnectorSaveInput {
  connector_type: string;
  type_version?: string;
  config?: Record<string, unknown>;
  mappings?: unknown;
  device_id?: string;
  profile_id?: string;
  health_check?: ConnectorHealthCheck;
}

// ConnectorSchema is one edge-registered config JSON Schema (migration 0043).
export interface ConnectorSchema {
  id: string;
  tenant_id: string;
  connector_type: string;
  connector_type_version: string;
  config_schema: Record<string, unknown>;
  source: string;
  // ADR-0032 optional protocol family/transports declared at registration.
  protocol?: string;
  transports?: string[];
  registered_by?: string;
  created_at: string;
  updated_at: string;
}

// ReconcileView is the read-only desired-vs-applied classification.
export interface ReconcileView {
  tenant_id: string;
  gateway_id: string;
  watermark?: {
    tenant_id: string;
    gateway_id: string;
    observed_at: string;
    spec_ids: string[];
  };
  in_sync: string[];
  missing: string[];
  stale: string[];
  drifted: string[];
  extra: string[];
  strict_mode: boolean;
}

// gatewayBase is the connector route prefix for one gateway device.
function gatewayBase(gatewayId: string): string {
  return `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}`;
}

// listConnectors fetches every desired spec (and reported state) for a gateway.
export async function listConnectors(gatewayId: string): Promise<ConnectorSpec[]> {
  const { data } = await client.get<ConnectorSpec[]>(`${gatewayBase(gatewayId)}/connectors`);
  return data;
}

// createConnector declares one desired connector instance.
export async function createConnector(
  gatewayId: string,
  input: ConnectorSaveInput,
): Promise<ConnectorSpec> {
  const { data } = await client.post<ConnectorSpec>(
    `${gatewayBase(gatewayId)}/connectors`,
    input,
  );
  return data;
}

// updateConnector changes one desired spec and bumps its revision.
export async function updateConnector(
  gatewayId: string,
  specId: string,
  input: ConnectorSaveInput,
): Promise<ConnectorSpec> {
  const { data } = await client.put<ConnectorSpec>(
    `${gatewayBase(gatewayId)}/connectors/${specId}`,
    input,
  );
  return data;
}

// deleteConnector removes one desired connector instance.
export async function deleteConnector(gatewayId: string, specId: string): Promise<void> {
  await client.delete(`${gatewayBase(gatewayId)}/connectors/${specId}`);
}

// getConnectorReconcile fetches the read-only drift classification.
export async function getConnectorReconcile(gatewayId: string): Promise<ReconcileView> {
  const { data } = await client.get<ReconcileView>(
    `${gatewayBase(gatewayId)}/connectors-reconcile`,
  );
  return data;
}

// listConnectorSchemas fetches the tenant's edge-registered config schemas.
export async function listConnectorSchemas(): Promise<ConnectorSchema[]> {
  const { data } = await client.get<ConnectorSchema[]>(
    `/v1/tenants/${currentTenantId()}/connector-schemas`,
  );
  return data;
}
