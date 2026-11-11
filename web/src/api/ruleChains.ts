// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ruleChains.ts exposes chain group management, per-group version calls
// and graph editing for the React Flow editor, plus the non-destructive
// dry-run test delivery.

import client from "./client";
import { currentTenantId } from "./tenant";


// groupsBase is the chain group REST prefix.
const groupsBase = () => `/v1/tenants/${currentTenantId()}/rule-chain-groups`;

// chainsBase is the chain version REST prefix.
const chainsBase = () => `/v1/tenants/${currentTenantId()}/rule-chains`;

// GroupMeta is one chain group with its version summary.
export interface GroupMeta {
  id: string;
  name: string;
  is_default: boolean;
  enabled: boolean;
  active_version: number;
  active_chain_id?: string;
  latest_version: number;
  device_count: number;
  profile_count: number;
  created_at: string;
}

// ChainMeta is one chain version without the graph.
export interface ChainMeta {
  id: string;
  group_id: string;
  name: string;
  version: number;
  state: "DRAFT" | "ACTIVE" | "ARCHIVED";
  is_root: boolean;
  created_at: string;
}

// NodeDTO is one graph node.
export interface NodeDTO {
  id: string;
  type: string;
  name: string;
  config: Record<string, unknown>;
  position_x?: number | null;
  position_y?: number | null;
}

// EdgeDTO is one graph edge.
export interface EdgeDTO {
  from: string;
  to: string;
  relation: string;
}

// GraphDTO is a full chain graph.
export interface GraphDTO {
  nodes: NodeDTO[];
  edges: EdgeDTO[];
}

// SwitchCase is one switch branch.
export interface SwitchCase {
  label: string;
  script: string;
}

// listGroups fetches all chain groups of the tenant.
export async function listGroups(): Promise<GroupMeta[]> {
  const { data } = await client.get<GroupMeta[]>(groupsBase());
  return data;
}

// createGroup creates an empty named chain group.
export async function createGroup(name: string): Promise<string> {
  const { data } = await client.post<{ id: string }>(groupsBase(), { name });
  return data.id;
}

// listChainVersions fetches all versions of one group.
export async function listChainVersions(groupId: string): Promise<ChainMeta[]> {
  const { data } = await client.get<ChainMeta[]>(
    `${groupsBase()}/${groupId}/versions`,
  );
  return data;
}

// getGraph fetches one chain version graph. Null segments (older rows
// saved before the []-never-null fix) are coerced to empty arrays so the
// editor's .map calls never crash.
export async function getGraph(chainId: string): Promise<GraphDTO> {
  const { data } = await client.get<GraphDTO>(`${chainsBase()}/${chainId}`);
  return { nodes: data.nodes ?? [], edges: data.edges ?? [] };
}

// createDraft stores a graph as a new draft version of a group.
export async function createDraft(
  groupId: string,
  graph: GraphDTO,
): Promise<{ chain_id: string; graph: GraphDTO }> {
  const { data } = await client.post(
    `${groupsBase()}/${groupId}/versions`,
    graph,
  );
  return {
    chain_id: data.chain_id,
    graph: { nodes: data.graph.nodes ?? [], edges: data.graph.edges ?? [] },
  };
}

// activateChain activates a draft version.
export async function activateChain(chainId: string): Promise<void> {
  await client.post(`${chainsBase()}/${chainId}/activate`);
}

// setGroupEnabled suspends or resumes execution of one chain group.
export async function setGroupEnabled(
  groupId: string,
  enabled: boolean,
): Promise<void> {
  await client.put(`${groupsBase()}/${groupId}`, { enabled });
}

// deleteGroup removes one unreferenced, non-default chain group. The API
// rejects deletion of the tenant default group or a group still bound to
// devices or profiles (409 RULE_CHAIN_GROUP_IN_USE).
export async function deleteGroup(groupId: string): Promise<void> {
  await client.delete(`${groupsBase()}/${groupId}`);
}

// dryRun executes one test message without side effects and returns the
// per-node trace. When graph is provided, it is compiled for this request;
// otherwise the tenant default active chain is tested.
export async function dryRun(
  graph: GraphDTO | undefined,
  data: Record<string, unknown>,
  metadata: Record<string, unknown>,
): Promise<{
  trace_id: string;
  records: {
    node_id: string;
    node_type: string;
    outputs?: string[];
    error?: string;
  }[];
}> {
  const { data: result } = await client.post(`${chainsBase()}/dry-run`, {
    data,
    metadata,
    graph,
  });
  return result;
}

// RuleMsg is the message body carried between nodes.
export interface RuleMsg {
  type?: string;
  tenant_id?: string;
  device_id?: string;
  ts?: number;
  trace_id?: string;
  data?: Record<string, unknown>;
  metadata?: Record<string, unknown>;
}

// RuleBranch is one labeled node output with its message.
export interface RuleBranch {
  relation: string;
  msg: RuleMsg;
}

// NodeTraceRecord is one node execution inside a live trace.
export interface NodeTraceRecord {
  node_id: string;
  node_type: string;
  entered?: string;
  duration_ms?: number;
  in: RuleMsg;
  out?: RuleBranch[];
  outputs?: string[];
  error?: string;
}

// ChainTrace is the per-message live trace recorded while tracing is on.
export interface ChainTrace {
  trace_id: string;
  chain_id: string;
  started: string;
  records: NodeTraceRecord[];
}

// setChainTrace enables or disables live tracing for one chain version.
// Only the currently active version receives real telemetry.
export async function setChainTrace(
  chainId: string,
  enabled: boolean,
): Promise<void> {
  await client.post(`${chainsBase()}/trace`, { enabled }, {
    params: { chain_id: chainId },
  });
}

// getChainTraces fetches recent live traces for one chain version.
export async function getChainTraces(chainId: string): Promise<ChainTrace[]> {
  const { data } = await client.get<ChainTrace[]>(`${chainsBase()}/trace`, {
    params: { chain_id: chainId },
  });
  return data ?? [];
}
