// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secretBindings.ts exposes the ADR-0034 edge secret delivery bindings:
// per-gateway desired bindings, delivery and withdrawal. Bindings never
// carry plaintext over REST; the list only shows desired state and reports.

import client from "./client";
import { currentTenantId } from "./tenant";

// BindingSelector is one generic target selector ([{kind,value}]).
export interface BindingSelector {
  kind: string;
  value: string;
}

// SecretBinding is one gateway/secret desired binding.
export interface SecretBinding {
  id: string;
  tenant_id: string;
  gateway_id: string;
  secret_name: string;
  secret_type: string;
  type_version: string;
  selectors: BindingSelector[];
  revision: number;
  status: string;
  reason?: string;
  dispatched_at?: string;
  applied_at?: string;
  removed_at?: string;
  created_at: string;
  updated_at: string;
}

// gatewayBase is the binding route prefix for one gateway device.
function gatewayBase(gatewayId: string): string {
  return `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}`;
}

// listSecretBindings fetches every active binding for a gateway.
export async function listSecretBindings(
  gatewayId: string,
): Promise<SecretBinding[]> {
  const { data } = await client.get<SecretBinding[]>(
    `${gatewayBase(gatewayId)}/secret-bindings`,
  );
  return data;
}

// deliverSecret upserts the desired binding for one named vault secret.
export async function deliverSecret(
  gatewayId: string,
  secretName: string,
  selectors: BindingSelector[],
): Promise<SecretBinding> {
  const { data } = await client.put<SecretBinding>(
    `${gatewayBase(gatewayId)}/secret-bindings/${encodeURIComponent(secretName)}`,
    { selectors },
  );
  return data;
}

// withdrawSecret removes the secret from the gateway on its next sync.
export async function withdrawSecret(
  gatewayId: string,
  secretName: string,
): Promise<void> {
  await client.delete(
    `${gatewayBase(gatewayId)}/secret-bindings/${encodeURIComponent(secretName)}`,
  );
}
