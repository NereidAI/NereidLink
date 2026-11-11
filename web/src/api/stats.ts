// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// stats.ts accesses the statistics center endpoints: the system
// infrastructure snapshot and the tenant business report.

import client from "./client";
import { currentTenantId } from "./tenant";

// MetricCount is a grouped label/count distribution point.
export interface MetricCount {
  label: string;
  count: number;
}

// MetricBucket is one time-bucketed series point.
export interface MetricBucket {
  time: string;
  count: number;
}

// GoRuntimeStats summarizes the Go process.
export interface GoRuntimeStats {
  goroutines: number;
  heap_alloc_bytes: number;
  sys_bytes: number;
  heap_objects: number;
  next_gc_bytes: number;
  gc_cycles: number;
  last_gc_pause_ms: number;
}

// PostgresStats summarizes the database and its pool.
export interface PostgresStats {
  max_connections: number;
  active_connections: number;
  total_connections: number;
  idle_connections: number;
  acquire_total: number;
  tx_commit: number;
  tx_rollback: number;
  block_hit_ratio: number;
  deadlocks: number;
}

// NATSStats summarizes the core NATS connection.
export interface NATSStats {
  in_msgs: number;
  out_msgs: number;
  in_bytes: number;
  out_bytes: number;
  reconnects: number;
}

// ConsumerStats describes one JetStream consumer.
export interface ConsumerStats {
  name: string;
  pending: number;
  redelivered: number;
  ack_floor_seq: number;
}

// StreamStats describes one JetStream stream.
export interface StreamStats {
  name: string;
  messages: number;
  bytes: number;
  first_seq: number;
  last_seq: number;
  consumer_count: number;
  consumers: ConsumerStats[];
}

// BrokerStats describes the embedded MQTT broker.
export interface BrokerStats {
  type: string;
  online_clients: number;
}

// SystemSnapshot is the full infrastructure snapshot.
export interface SystemSnapshot {
  generated_at: string;
  uptime_sec: number;
  go: GoRuntimeStats;
  postgres: PostgresStats;
  nats: NATSStats;
  streams: StreamStats[];
  broker: BrokerStats;
}

// DeviceBusiness summarizes the fleet.
export interface DeviceBusiness {
  total: number;
  online: number;
  offline: number;
  by_profile: MetricCount[];
}

// TelemetryBusiness summarizes telemetry volume.
export interface TelemetryBusiness {
  total: number;
  series: MetricBucket[];
}

// AlarmBusiness summarizes alarm counts.
export interface AlarmBusiness {
  created: number;
  live: number;
  by_state: MetricCount[];
  by_severity: MetricCount[];
}

// CommandBusiness summarizes command counts.
export interface CommandBusiness {
  created: number;
  by_state: MetricCount[];
}

// BusinessReport is the tenant business response.
export interface BusinessReport {
  range: string;
  since: string;
  generated_at: string;
  devices: DeviceBusiness;
  telemetry: TelemetryBusiness;
  alarms: AlarmBusiness;
  commands: CommandBusiness;
  active_rule_chains: number;
}

// BusinessRange selects the report time window.
export type BusinessRange = "1h" | "24h" | "7d";


// getSystemStats fetches the infrastructure snapshot.
export function getSystemStats(): Promise<SystemSnapshot> {
  return client
    .get(`/v1/tenants/${currentTenantId()}/stats/system`)
    .then((response) => response.data as SystemSnapshot);
}

// getBusinessStats fetches the tenant business report.
export function getBusinessStats(
  range: BusinessRange,
): Promise<BusinessReport> {
  return client
    .get(`/v1/tenants/${currentTenantId()}/stats/business?range=${range}`)
    .then((response) => response.data as BusinessReport);
}
