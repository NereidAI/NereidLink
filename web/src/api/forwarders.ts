// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// forwarders.ts exposes third-party data forwarding management (ADR-0018):
// CRUD over tenant forwarders and a read/replay surface over the forwarder
// dead-letter queue. The actual delivery is asynchronous and never appears
// on the telemetry hot path.

import client from "./client";
import { currentTenantId } from "./tenant";

// Forwarder is one forwarding configuration.
export interface Forwarder {
  id: string;
  tenant_id: string;
  name: string;
  sink_type: string;
  sink_config: Record<string, unknown>;
  encoder: string;
  max_attempts: number;
  message_ttl_ms: number;
  max_buffer_messages: number;
  enabled: boolean;
  created_by?: string;
  created_at: string;
  updated_at: string;
}

// ForwarderInput is the create/update payload.
export interface ForwarderInput {
  name: string;
  sink_type: string;
  sink_config: Record<string, unknown>;
  encoder?: string;
  max_attempts?: number;
  message_ttl_ms?: number;
  max_buffer_messages?: number;
  enabled?: boolean;
}

// DeadLetter is one parked envelope.
export interface DeadLetter {
  id: string;
  tenant_id: string;
  forwarder_id: string;
  payload: EgressEnvelope;
  error: string;
  attempts: number;
  last_tried_at?: string;
  replayed_at?: string;
  created_at: string;
}

// EgressEnvelope is the JSON frame carried on an egress stream.
export interface EgressEnvelope {
  id: string;
  tenant_id: string;
  device_id: string;
  ts: number;
  trace_id?: string;
  data: Record<string, unknown>;
  metadata?: Record<string, unknown>;
}

// Delivery is one per-forwarder delivery-log row.
export interface Delivery {
  id: string;
  tenant_id: string;
  forwarder_id: string;
  envelope_id: string;
  device_id: string;
  status: "delivered" | "retry" | "terminal";
  code: number;
  reason?: string;
  attempts: number;
  occurred_at: string;
}

// base returns the tenant forwarder route prefix.
function base(): string {
  return `/v1/tenants/${currentTenantId()}/forwarders`;
}

// listForwarders fetches every forwarder for the current tenant.
export async function listForwarders(): Promise<Forwarder[]> {
  const { data } = await client.get<Forwarder[]>(base());
  return data;
}

// createForwarder declares a new forwarder.
export async function createForwarder(
  input: ForwarderInput,
): Promise<Forwarder> {
  const { data } = await client.post<Forwarder>(base(), input);
  return data;
}

// updateForwarder applies an edit to one forwarder.
export async function updateForwarder(
  id: string,
  input: ForwarderInput,
): Promise< Forwarder> {
  const { data } = await client.put<Forwarder>(`${base()}/${id}`, input);
  return data;
}

// deleteForwarder removes one forwarder.
export async function deleteForwarder(id: string): Promise<void> {
  await client.delete(`${base()}/${id}`);
}

// listForwarderDLQ fetches dead letters, optionally for one forwarder.
export async function listForwarderDLQ(
  forwarderId = "",
): Promise<DeadLetter[]> {
  const query = forwarderId
    ? `?forwarder=${encodeURIComponent(forwarderId)}`
    : "";
  const { data } = await client.get<DeadLetter[]>(
    `/v1/tenants/${currentTenantId()}/forwarder-dlq${query}`,
  );
  return data;
}

// replayDeadLetter re-publishes one dead-letter envelope.
export async function replayDeadLetter(dlqId: string): Promise<void> {
  await client.post(
    `/v1/tenants/${currentTenantId()}/forwarder-dlq/${dlqId}/replay`,
  );
}

// listForwarderDeliveries fetches delivery-log rows, optionally for one
// forwarder, newest first.
export async function listForwarderDeliveries(
  forwarderId = "",
): Promise<Delivery[]> {
  const query = forwarderId
    ? `?forwarder=${encodeURIComponent(forwarderId)}`
    : "";
  const { data } = await client.get<Delivery[]>(
    `/v1/tenants/${currentTenantId()}/forwarder-deliveries${query}`,
  );
  return data;
}