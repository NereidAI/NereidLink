// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ownership.ts exposes the ADR-0013 ownership and lifecycle API calls:
// customers and device groups, claim tokens, claiming, transfer and
// decommission/reactivate.

import client from "./client";
import { currentTenantId } from "./tenant";

const tenantBase = () => `/v1/tenants/${currentTenantId()}`;

// Customer is one tenant-owned customer.
export interface Customer {
  id: string;
  name: string;
  contact_name?: string;
  contact_email?: string;
  external_id?: string;
}

// DeviceGroup is one node in the group tree.
export interface DeviceGroup {
  id: string;
  customer_id?: string;
  parent_id?: string;
  name: string;
}

// ClaimToken is the single-use token returned at issuance.
export interface ClaimToken {
  device_id: string;
  token: string;
  expires_at: string;
}

// ClaimPayload claims a device with a token.
export interface ClaimPayload {
  token: string;
  customer_id?: string;
  group_id?: string;
}

// TransferPayload moves ownership of a device.
export interface TransferPayload {
  customer_id: string;
  group_id?: string;
  reset_credentials: boolean;
  alarm_policy: "retain" | "clear";
}

// listCustomers returns every customer of the tenant.
export async function listCustomers(): Promise<Customer[]> {
  const { data } = await client.get<Customer[]>(`${tenantBase()}/customers`);
  return data;
}

// createCustomer creates one customer.
export async function createCustomer(input: Omit<Customer, "id">): Promise<Customer> {
  const { data } = await client.post<Customer>(`${tenantBase()}/customers`, input);
  return data;
}

// updateCustomer updates one customer.
export async function updateCustomer(id: string, input: Omit<Customer, "id">): Promise<Customer> {
  const { data } = await client.put<Customer>(
    `${tenantBase()}/customers/${id}`,
    input,
  );
  return data;
}

// deleteCustomer removes one customer.
export async function deleteCustomer(id: string): Promise<void> {
  await client.delete(`${tenantBase()}/customers/${id}`);
}

// listDeviceGroups returns every device group of the tenant.
export async function listDeviceGroups(): Promise<DeviceGroup[]> {
  const { data } = await client.get<DeviceGroup[]>(`${tenantBase()}/device-groups`);
  return data;
}

// createDeviceGroup creates one group.
export async function createDeviceGroup(input: Omit<DeviceGroup, "id">): Promise<DeviceGroup> {
  const { data } = await client.post<DeviceGroup>(`${tenantBase()}/device-groups`, input);
  return data;
}

// updateDeviceGroup updates one group.
export async function updateDeviceGroup(id: string, input: Omit<DeviceGroup, "id">): Promise<void> {
  await client.put(`${tenantBase()}/device-groups/${id}`, input);
}

// deleteDeviceGroup removes one group.
export async function deleteDeviceGroup(id: string): Promise<void> {
  await client.delete(`${tenantBase()}/device-groups/${id}`);
}

// issueClaimToken creates a single-use claim token for a device.
export async function issueClaimToken(deviceId: string, ttl?: string): Promise<ClaimToken> {
  const { data } = await client.post<ClaimToken>(
    `${tenantBase()}/devices/${deviceId}/claim-token`,
    ttl ? { ttl } : {},
  );
  return data;
}

// claimDevice consumes a claim token.
export async function claimDevice(deviceId: string, payload: ClaimPayload): Promise<void> {
  await client.post(`${tenantBase()}/devices/${deviceId}/claim`, payload);
}

// transferDevice moves ownership of a device.
export async function transferDevice(deviceId: string, payload: TransferPayload): Promise<void> {
  await client.post(`${tenantBase()}/devices/${deviceId}/transfer`, payload);
}

// decommissionDevice retires a device into its recall window.
export async function decommissionDevice(deviceId: string, recall?: string): Promise<void> {
  await client.post(
    `${tenantBase()}/devices/${deviceId}/decommission`,
    recall ? { recall } : {},
  );
}

// reactivateDevice restores a decommissioned device.
export async function reactivateDevice(deviceId: string): Promise<void> {
  await client.post(`${tenantBase()}/devices/${deviceId}/reactivate`, {});
}

// quarantineDevice enters incident-response quarantine; the device can no
// longer connect until released. reasonCode must come from
// fetchQuarantineReasonCodes; note is mandatory for the "other" code.
export async function quarantineDevice(
  deviceId: string, reasonCode: string, note: string,
): Promise<void> {
  await client.post(`${tenantBase()}/devices/${deviceId}/quarantine`, {
    reason_code: reasonCode,
    note,
  });
}

// releaseFromQuarantine returns a quarantined device to ACTIVE state.
export async function releaseFromQuarantine(deviceId: string, note: string): Promise<void> {
  await client.post(`${tenantBase()}/devices/${deviceId}/quarantine/release`, { note });
}

// quarantineReasonResponse is the payload returned by the reason codes endpoint.
export interface QuarantineReasonResponse {
  // reason_codes lists every accepted structured code.
  reason_codes: string[]
  // note_required lists codes for which a free-text note is mandatory.
  note_required: string[]
}

// fetchQuarantineReasonCodes returns the accepted quarantine reason codes.
export async function fetchQuarantineReasonCodes(): Promise<QuarantineReasonResponse> {
  const { data } = await client.get<QuarantineReasonResponse>(
    `${tenantBase()}/devices/quarantine-reasons`,
  );
  return data;
}

// customerDeviceCounts returns device counts keyed by customer id.
export async function customerDeviceCounts(): Promise<Record<string, number>> {
  const { data } = await client.get<Record<string, number>>(
    `${tenantBase()}/customer-device-counts`,
  );
  return data;
}

// groupDeviceCounts returns device counts keyed by device group id.
export async function groupDeviceCounts(): Promise<Record<string, number>> {
  const { data } = await client.get<Record<string, number>>(
    `${tenantBase()}/group-device-counts`,
  );
  return data;
}
