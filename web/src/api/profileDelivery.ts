// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profileDelivery.ts exposes the ADR-0038 device-profile downlink surface:
// distribute a tenant library profile to explicit gateways, withdraw a
// distribution edge, and read per-gateway delivery state. The platform only
// ever references a library entry plus its revision; the document is rendered
// cloud-side in the format each gateway declared, so none of these calls carry
// profile source text.

import client from "./client";
import { currentTenantId } from "./tenant";

// ProfileTarget is one explicit (profile -> gateway) distribution edge.
export interface ProfileTarget {
  profile_id: string;
  gateway_id: string;
  gateway_name?: string;
  created_at: string;
}

// ProfileDelivery is one (profile, gateway) delivery state row.
export interface ProfileDelivery {
  tenant_id: string;
  profile_id: string;
  profile_name: string;
  gateway_id: string;
  desired_revision: number;
  dispatched_revision: number;
  applied_revision: number;
  state: string;
  reason?: string;
  // explicit marks an operator-created distribution edge; false means the
  // gateway only holds the profile through a connector instance (not
  // withdrawable from the downlink surface).
  explicit: boolean;
  desired_at: string;
  dispatched_at?: string;
  applied_at?: string;
  failed_at?: string;
  updated_at: string;
}

// profileBase is the profile-scoped REST prefix.
function profileBase(profileId: string): string {
  return `/v1/tenants/${currentTenantId()}/profiles/${profileId}`;
}

// gatewayBase is the gateway-scoped REST prefix.
function gatewayBase(gatewayId: string): string {
  return `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}`;
}

// listProfileTargets fetches the explicit distribution edges of one profile.
export async function listProfileTargets(
  profileId: string,
): Promise<ProfileTarget[]> {
  const { data } = await client.get<ProfileTarget[]>(
    `${profileBase(profileId)}/targets`,
  );
  return data;
}

// distributeProfile adds an explicit distribution edge, pushing the library
// profile to the gateway on its next sync. The call returns the refreshed
// target set (HTTP 201).
export async function distributeProfile(
  profileId: string,
  gatewayId: string,
): Promise<ProfileTarget[]> {
  const { data } = await client.post<ProfileTarget[]>(
    `${profileBase(profileId)}/targets`,
    { gateway_id: gatewayId },
  );
  return data;
}

// withdrawProfileTarget removes one explicit distribution edge. When no
// connector instance still references the profile, the gateway is told to
// remove its copy on the next sync.
export async function withdrawProfileTarget(
  profileId: string,
  gatewayId: string,
): Promise<void> {
  await client.delete(`${profileBase(profileId)}/targets/${gatewayId}`);
}

// listDeliveriesForProfile fetches a profile's delivery state across every
// gateway (explicit targets plus connector-referenced gateways).
export async function listDeliveriesForProfile(
  profileId: string,
): Promise<ProfileDelivery[]> {
  const { data } = await client.get<ProfileDelivery[]>(
    `${profileBase(profileId)}/deliveries`,
  );
  return data;
}

// listProfileDeliveriesForGateway fetches every profile delivery state for one
// gateway (the gateway detail "Profile delivery" tab).
export async function listProfileDeliveriesForGateway(
  gatewayId: string,
): Promise<ProfileDelivery[]> {
  const { data } = await client.get<ProfileDelivery[]>(
    `${gatewayBase(gatewayId)}/profile-deliveries`,
  );
  return data;
}
