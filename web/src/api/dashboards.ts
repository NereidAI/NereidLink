// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// dashboards.ts is the client for user-defined dashboard CRUD (ADR-0020).
// The layout is an array of grid items carrying widget type, geometry and
// binding config; it mirrors internal/dashboards Widget/Dashboard types.

import client from "./client";
import { currentTenantId } from "./tenant";

// Widget is one grid item with its binding config.
export interface Widget {
  id: string;
  type: "timeseries" | "stat" | "alarms";
  title?: string;
  device_id?: string;
  measurement_key?: string;
  range?: string;
  x: number;
  y: number;
  w: number;
  h: number;
}

// Dashboard is one persisted dashboard.
export interface Dashboard {
  id: string;
  tenant_id: string;
  name: string;
  sort_order: number;
  layout: Widget[];
  created_at: string;
  updated_at: string;
}

// DashboardInput carries the writable fields.
export interface DashboardInput {
  name: string;
  sort_order?: number;
  layout: Widget[];
}

// base builds the tenant dashboard prefix.
const base = () => `/v1/tenants/${currentTenantId()}/dashboards`;

// listDashboards returns every dashboard of the tenant.
export async function listDashboards(): Promise<Dashboard[]> {
  const { data } = await client.get<Dashboard[]>(base());
  return data;
}

// createDashboard persists a new dashboard.
export async function createDashboard(
  input: DashboardInput,
): Promise<Dashboard> {
  const { data } = await client.post<Dashboard>(base(), input);
  return data;
}

// updateDashboard replaces one dashboard.
export async function updateDashboard(
  id: string,
  input: DashboardInput,
): Promise<Dashboard> {
  const { data } = await client.put<Dashboard>(`${base()}/${id}`, input);
  return data;
}

// deleteDashboard removes one dashboard.
export async function deleteDashboard(id: string): Promise<void> {
  await client.delete(`${base()}/${id}`);
}
