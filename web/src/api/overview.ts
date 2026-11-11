// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// overview.ts exposes the aggregated tenant operational snapshot used by
// the Overview dashboard.

import client from "./client";
import { currentTenantId } from "./tenant";


// MetricCount is one labeled chart count.
export interface MetricCount {
  label: string;
  count: number;
}

// RecentAlarm is one compact alarm row.
export interface RecentAlarm {
  id: string;
  device_id: string;
  type: string;
  severity: string;
  state: string;
  started_at: string;
}

// RecentNotification is one compact inbox row.
export interface RecentNotification {
  id: string;
  device_id: string;
  title: string;
  body: string;
  severity: string;
  state: string;
  created_at: string;
}

// OverviewSummary is the tenant dashboard snapshot.
export interface OverviewSummary {
  generated_at: string;
  device_total: number;
  online_devices: number;
  offline_devices: number;
  live_alarms: number;
  pending_commands: number;
  unread_notifications: number;
  telemetry_last_hour: number;
  telemetry_last_24h: number;
  telemetry_buckets: MetricCount[];
  alarm_severities: MetricCount[];
  recent_alarms: RecentAlarm[];
  recent_notifications: RecentNotification[];
}

// getOverview fetches the current tenant operational snapshot.
export async function getOverview(): Promise<OverviewSummary> {
  const { data } = await client.get<OverviewSummary>(
    `/v1/tenants/${currentTenantId()}/overview`,
  );
  return data;
}
