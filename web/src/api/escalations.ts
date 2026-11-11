// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// escalations.ts exposes alarm escalation chain CRUD (ADR-0019).

import client from "./client";
import { currentTenantId } from "./tenant";

// chainBase is the escalation chain REST prefix.
const chainBase = () => `/v1/tenants/${currentTenantId()}/escalation-chains`;

// EscalationStep is one ordered step of a chain.
export interface EscalationStep {
  step_id?: string;
  order: number;
  delay_seconds: number;
  target_role: string;
  target_group_id?: string;
  stop_on_ack: boolean;
  stop_on_clear: boolean;
}

// EscalationChain is one chain with its ordered steps.
export interface EscalationChain {
  chain_id: string;
  tenant_id: string;
  name: string;
  enabled: boolean;
  repeat_interval_seconds: number;
  repeat_target_role: string;
  repeat_target_group_id?: string;
  steps: EscalationStep[];
  created_at: string;
  updated_at: string;
}

// EscalationStepInput carries one step on create/update.
export interface EscalationStepInput {
  delay_seconds: number;
  target_role: string;
  target_group_id?: string;
  stop_on_ack: boolean;
  stop_on_clear: boolean;
}

// EscalationSaveInput carries the chain fields on create/update.
export interface EscalationSaveInput {
  name: string;
  enabled?: boolean;
  repeat_interval_seconds: number;
  repeat_target_role?: string;
  repeat_target_group_id?: string;
  steps: EscalationStepInput[];
}

// listEscalationChains fetches all chains of the tenant.
export async function listEscalationChains(): Promise<EscalationChain[]> {
  const { data } = await client.get<EscalationChain[]>(chainBase());
  return data;
}

// createEscalationChain creates one chain.
export async function createEscalationChain(
  input: EscalationSaveInput,
): Promise<EscalationChain> {
  const { data } = await client.post<EscalationChain>(chainBase(), input);
  return data;
}

// updateEscalationChain replaces one chain.
export async function updateEscalationChain(
  id: string,
  input: EscalationSaveInput,
): Promise<EscalationChain> {
  const { data } = await client.put<EscalationChain>(`${chainBase()}/${id}`, input);
  return data;
}

// deleteEscalationChain deletes one chain.
export async function deleteEscalationChain(id: string): Promise<void> {
  await client.delete(`${chainBase()}/${id}`);
}
