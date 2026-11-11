// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// devices.ts exposes typed registry, telemetry and command API calls used
// by the device list and detail pages.

import client from "./client";
import { currentTenantId } from "./tenant";
import { multipartUpload, type ImportPreview } from "./imports";


// tenantBase builds the REST prefix of the session-resolved tenant.
const tenantBase = () => `/v1/tenants/${currentTenantId()}`;

// Device is a registry device row.
export interface Device {
  id: string;
  canonical_name: string;
  profile_id?: string;
  rule_chain_group_id?: string;
  customer_id?: string;
  group_id?: string;
  is_gateway?: boolean;
  gateway_id?: string;
  gateway_name?: string;
  connector_id?: string;
  connector_type?: string;
  connector_state?: string;
  gateway_connector_count?: number;
  firmware_version?: string;
  agent_features?: string[];
  agent_operations?: string[];
  state: "UNCLAIMED" | "ACTIVE" | "DECOMMISSIONED" | "ARCHIVED" | "QUARANTINED";
  claimed_at?: string;
  decommissioned_at?: string;
  recall_until?: string;
  presence_state?: "ONLINE" | "OFFLINE";
  presence_since?: string;
  created_at?: string;
  updated_at?: string;
  alias?: string;
  notes?: string;
  last_activity_at?: string;
}

// Profile is a device profile row used for filter/labels.
export interface Profile {
  id: string;
  name: string;
}

// Page is the standard server pagination envelope.
export interface Page<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
  total_pages: number;
}

// Attribute is one device shadow entry.
export interface Attribute {
  scope: string;
  key: string;
  value: unknown;
  updated_at: string;
}

// CommandRecord is one command audit row.
export interface CommandRecord {
  id: string;
  device_id: string;
  name: string;
  interaction: string;
  timeliness: string;
  direction?: string;
  params: string;
  state: string;
  response?: string;
  error?: string;
  created_at: string;
  delivered_at?: string;
  finished_at?: string;
}

// TelemetryPoint is one historical telemetry row.
export interface TelemetryHistoryPoint {
  time: string;
  measurement_key: string;
  v_double?: number | null;
  raw_value?: number | null;
  unit?: string;
  descriptor_version?: number | null;
  v_string?: string;
  v_bool?: boolean | null;
  quality: string;
}

// AlarmRow is one alarm audit row.
export interface AlarmRow {
  alarm_id: string;
  device_id: string;
  device_name?: string;
  alarm_type: string;
  severity: string;
  state: string;
  details?: string;
  started_at: string;
  acked_at?: string;
  cleared_at?: string;
  assignee_id?: string;
  assignee_name?: string;
}

// ListDevicesParams carries filtering and pagination.
export interface ListDevicesParams {
  search?: string;
  profile_id?: string;
  customer_id?: string;
  group_id?: string;
  state?: string;
  connection?: string;
  gateway?: boolean;
  managed_by?: string;
  type?: string;
  page: number;
  page_size: number;
}

// listDevicesByGateway fetches the sub-devices managed by one gateway.
export async function listDevicesByGateway(
  gatewayId: string,
): Promise<Device[]> {
  const { data } = await client.get<Page<Device>>(
    `${tenantBase()}/devices`,
    { params: { managed_by: gatewayId, page: 1, page_size: 500 } },
  );
  return data.items;
}

// listDevices fetches one page of devices.
export async function listDevices(
  params: ListDevicesParams,
): Promise<Page<Device>> {
  const { data } = await client.get<Page<Device>>(
    `${tenantBase()}/devices`,
    { params },
  );
  return data;
}

// listProfiles fetches all profiles (small set in the MVP).
export async function listProfiles(): Promise<Profile[]> {
  const { data } = await client.get<Profile[]>(
    `${tenantBase()}/profiles`,
  );
  return data;
}

// DeviceInput carries the writable device fields.
export interface DeviceInput {
  canonical_name: string;
  profile_id?: string;
  rule_chain_group_id?: string;
  customer_id?: string;
  group_id?: string;
  is_gateway?: boolean;
  gateway_id?: string;
  state?: "UNCLAIMED" | "ACTIVE";
  alias?: string;
  notes?: string;
}

// listGatewayDevices fetches every ACTIVE gateway device, used by the
// sub-device binding picker in the create/edit form.
export async function listGatewayDevices(): Promise<Device[]> {
  const { data } = await client.get<Page<Device>>(
    `${tenantBase()}/devices`,
    { params: { gateway: true, state: "ACTIVE", page: 1, page_size: 500 } },
  );
  return data.items;
}

// createDevice creates one device.
export async function createDevice(input: DeviceInput): Promise<Device> {
  const { data } = await client.post<Device>(
    `${tenantBase()}/devices`,
    input,
  );
  return data;
}

// updateDevice updates one device.
export async function updateDevice(
  id: string,
  input: DeviceInput,
): Promise<Device> {
  const { data } = await client.put<Device>(
    `${tenantBase()}/devices/${id}`,
    input,
  );
  return data;
}

// deleteDevice deletes one device.
export async function deleteDevice(id: string): Promise<void> {
  await client.delete(`${tenantBase()}/devices/${id}`);
}

// reactivateDevice restores a decommissioned device to ACTIVE while it is
// still inside its recall window.
export async function reactivateDevice(id: string): Promise<Device> {
  const { data } = await client.post<Device>(
    `${tenantBase()}/devices/${id}/reactivate`,
  );
  return data;
}

// getDevice fetches one device by id.
export async function getDevice(id: string): Promise<Device> {
  const { data } = await client.get<Device>(`${tenantBase()}/devices/${id}`);
  return data;
}

// listAttributes fetches the attributes of a device.
export async function listAttributes(deviceId: string): Promise<Attribute[]> {
  const { data } = await client.get<Attribute[]>(
    `${tenantBase()}/devices/${deviceId}/attributes`,
  );
  return data;
}

// putAttribute upserts one server/shared attribute.
export async function putAttribute(
  deviceId: string,
  scope: string,
  key: string,
  value: unknown,
): Promise<void> {
  await client.put(
    `${tenantBase()}/devices/${deviceId}/attributes`,
    { scope, key, value },
  );
}

// deleteAttribute removes one platform-writable (SERVER/SHARED) attribute.
// The platform fans a null value out so a live edge drops the key locally.
export async function deleteAttribute(
  deviceId: string,
  scope: string,
  key: string,
): Promise<void> {
  await client.delete(`${tenantBase()}/devices/${deviceId}/attributes`, {
    params: { scope, key },
  });
}

// listCommandHistory fetches one page of commands for a device.
export async function listCommandHistory(
  deviceId: string,
  page: number,
  pageSize: number,
  state?: string,
): Promise<Page<CommandRecord>> {
  const { data } = await client.get<Page<CommandRecord>>(
    `${tenantBase()}/devices/${deviceId}/commands`,
    { params: { page, page_size: pageSize, state } },
  );
  return data;
}

// sendCommand dispatches one command to a device.
export async function sendCommand(
  deviceId: string,
  name: string,
  params: unknown,
  direction?: "read" | "write",
  timeoutMs?: number,
): Promise<CommandResult> {
  const { data } = await client.post<CommandResult>(
    `${tenantBase()}/devices/${deviceId}/commands`,
    { name, params, direction, timeout_ms: timeoutMs ?? 0 },
  );
  return data;
}

// CommandResult is the synchronous dispatch result.
export interface CommandResult {
  command_id: string;
  status: string;
  response?: unknown;
  error?: string;
}

// CommandCatalogEntry is one declared (whitelisted) command.
export interface CommandCatalogEntry {
  name: string;
  interaction: "one-way" | "request-response";
  timeliness: "realtime" | "persistent";
  default_timeout_ms: number;
  allow_read: boolean;
  allow_write: boolean;
  enabled: boolean;
  source: "manual" | "auto";
  description?: string;
  created_at: string;
}

// CommandCatalogInput creates or replaces one declaration.
export interface CommandCatalogInput {
  name: string;
  interaction: "one-way" | "request-response";
  timeliness: "realtime" | "persistent";
  default_timeout_ms: number;
  allow_read: boolean;
  allow_write: boolean;
  enabled?: boolean;
  description?: string;
}

// listCommandCatalog returns the device command whitelist.
export async function listCommandCatalog(
  deviceId: string,
): Promise<CommandCatalogEntry[]> {
  const { data } = await client.get<{ items: CommandCatalogEntry[] }>(
    `${tenantBase()}/devices/${deviceId}/command-catalog`,
  );
  return data.items ?? [];
}

// createCommandCatalogEntry declares one command.
export async function createCommandCatalogEntry(
  deviceId: string,
  input: CommandCatalogInput,
): Promise<CommandCatalogEntry> {
  const { data } = await client.post<CommandCatalogEntry>(
    `${tenantBase()}/devices/${deviceId}/command-catalog`,
    input,
  );
  return data;
}

// updateCommandCatalogEntry replaces one declaration.
export async function updateCommandCatalogEntry(
  deviceId: string,
  name: string,
  input: CommandCatalogInput,
): Promise<CommandCatalogEntry> {
  const { data } = await client.put<CommandCatalogEntry>(
    `${tenantBase()}/devices/${deviceId}/command-catalog/${encodeURIComponent(name)}`,
    input,
  );
  return data;
}

// deleteCommandCatalogEntry removes one declaration.
export async function deleteCommandCatalogEntry(
  deviceId: string,
  name: string,
): Promise<void> {
  await client.delete(
    `${tenantBase()}/devices/${deviceId}/command-catalog/${encodeURIComponent(name)}`,
  );
}

// TelemetryRange selects the historical telemetry window in milliseconds.
export type TelemetryRange =
  | "5m"
  | "15m"
  | "30m"
  | "1h"
  | "2h"
  | "6h"
  | "12h"
  | "24h"
  | "7d";

// TelemetryValueType discriminates the value carrier of a measurement.
export type TelemetryValueType = "number" | "boolean" | "string";

// TelemetryKey describes the most recent value of one measurement key.
export interface TelemetryKey {
  measurement_key: string;
  value_type: TelemetryValueType;
  unit?: string;
  time: string;
  v_double?: number;
  v_bool?: boolean;
  v_string?: string;
  quality?: string;
}

// telemetryRangeMS maps each preset onto its duration in milliseconds.
const telemetryRangeMS: Record<TelemetryRange, number> = {
  "5m": 300_000,
  "15m": 900_000,
  "30m": 1_800_000,
  "1h": 3_600_000,
  "2h": 7_200_000,
  "6h": 21_600_000,
  "12h": 43_200_000,
  "24h": 86_400_000,
  "7d": 604_800_000,
};

// listTelemetry fetches a page of historical telemetry for a device. The
// optional range bounds the query window; when omitted the server applies
// its default 24h window.
export async function listTelemetry(
  deviceId: string,
  page: number,
  pageSize: number,
  key?: string,
  range?: TelemetryRange,
): Promise<Page<TelemetryHistoryPoint>> {
  const params: Record<string, string | number> = {
    page,
    page_size: pageSize,
  };
  if (key) {
    params.key = key;
  }
  if (range) {
    const end = Date.now();
    params.end = new Date(end).toISOString();
    params.start = new Date(end - telemetryRangeMS[range]).toISOString();
  }
  const { data } = await client.get<Page<TelemetryHistoryPoint>>(
    `${tenantBase()}/devices/${deviceId}/telemetry`,
    { params },
  );
  return data;
}

// listTelemetryKeys discovers the measurement keys of a device with the
// latest value of each key.
export async function listTelemetryKeys(
  deviceId: string,
): Promise<TelemetryKey[]> {
  const { data } = await client.get<TelemetryKey[]>(
    `${tenantBase()}/devices/${deviceId}/telemetry/keys`,
  );
  return data;
}

// TelemetrySeriesPoint is one downsampled bucket value.
export interface TelemetrySeriesPoint {
  time: string;
  value_type: TelemetryValueType;
  v_double?: number;
  v_bool?: boolean;
  v_string?: string;
}

// listTelemetrySeries fetches downsampled values covering the window. The
// raw history page only spans the newest N rows; this groups the window into
// buckets so long ranges render correctly.
export async function listTelemetrySeries(
  deviceId: string,
  key: string,
  range: TelemetryRange,
): Promise<TelemetrySeriesPoint[]> {
  const end = Date.now();
  const { data } = await client.get<TelemetrySeriesPoint[]>(
    `${tenantBase()}/devices/${deviceId}/telemetry/series`,
    {
      params: {
        key,
        end: new Date(end).toISOString(),
        start: new Date(end - telemetryRangeMS[range]).toISOString(),
      },
    },
  );
  return data;
}

// listDeviceAlarms fetches the recent alarms of a device.
export async function listDeviceAlarms(
  deviceId: string,
): Promise<AlarmRow[]> {
  const { data } = await client.get<Page<AlarmRow>>(
    `${tenantBase()}/alarms`,
    { params: { device_id: deviceId, state: "LIVE", page: 1, page_size: 20 } },
  );
  return data.items;
}

// BulkImportFailure is one rejected row in a bulk import result.
export interface BulkImportFailure {
  line: number;
  canonical_name?: string;
  reason: string;
}

// BulkImportResult summarises a bulk import upload.
export interface BulkImportResult {
  total: number;
  created: number;
  failed: BulkImportFailure[];
}

// bulkImportDevices uploads a CSV or XLSX file and creates devices from it.
// Accepts a File object (CSV or XLSX) and sends multipart/form-data.
//
// Content-Type is deliberately left unset. When a browser sees FormData it
// generates the multipart boundary itself and writes the header including
// that boundary. Setting the header by hand omits the boundary, so the server
// cannot parse the parts and the upload fails.
export async function bulkImportDevices(file: File): Promise<BulkImportResult> {
  return multipartUpload<BulkImportResult>(
    `${tenantBase()}/devices/bulk-import`,
    file,
  );
}

// previewDeviceImport validates a CSV/XLSX upload and returns the parsed
// rows without creating anything, used by the import preview step.
export async function previewDeviceImport(file: File): Promise<ImportPreview> {
  return multipartUpload<ImportPreview>(
    `${tenantBase()}/devices/bulk-import/preview`,
    file,
  );
}

// bulkImportTemplateUrl returns the download URL for the import template.
// format: "xlsx" (default) or "csv".
export function bulkImportTemplateUrl(format: "xlsx" | "csv" = "xlsx"): string {
  return `${tenantBase()}/devices/bulk-import/template?format=${format}`;
}

// BulkDecommissionResult summarises a bulk decommission action.
export interface BulkDecommissionResult {
  decommissioned: number;
  skipped: { device_id: string; reason: string }[];
}

// bulkDecommissionDevices decommissions many devices at once.
export async function bulkDecommissionDevices(
  deviceIds: string[],
  recallDays?: number,
): Promise<BulkDecommissionResult> {
  const { data } = await client.post<BulkDecommissionResult>(
    `${tenantBase()}/devices/bulk-decommission`,
    { device_ids: deviceIds, recall_days: recallDays },
  );
  return data;
}
