// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// edgeProfiles.ts exposes the EdgeX device profiles a gateway reports on
// v1/gateway/profiles and the explicit adopt action that turns one into a
// native device profile.

import client from "./client";
import { currentTenantId } from "./tenant";

// EdgeProfile is one cached EdgeX profile reported by a gateway.
export interface EdgeProfile {
  name: string;
  content_hash: string;
  resource_count: number;
  first_seen_at: string;
  last_seen_at: string;
  adopted_profile_id?: string;
  adopted_at?: string | null;
}

// EdgeProfileResource is one raw deviceResource with its adopt decision.
export interface EdgeProfileResource {
  name: string;
  value_type: string;
  units: string;
  description: string;
  translatable: boolean;
  skip_reason?: string;
}

// EdgeProfileDetail is one cached profile including every raw resource.
export interface EdgeProfileDetail extends EdgeProfile {
  resources: EdgeProfileResource[];
}

// AdoptMode controls conflict handling on adopt.
export type AdoptMode = "" | "update";

// AdoptEdgeProfileInput is the optional adopt request body.
export interface AdoptEdgeProfileInput {
  mode?: AdoptMode;
  target_name?: string;
}

// SkippedResource is a deviceResource kept in the cache but not translated.
export interface SkippedResource {
  name: string;
  value_type: string;
  reason: string;
}

// AdoptEdgeProfileResult reports the adopt outcome.
export interface AdoptEdgeProfileResult {
  profile_id: string;
  profile_name: string;
  created: boolean;
  updated: boolean;
  adopted_count: number;
  skipped: SkippedResource[];
}

function gatewayBase(gatewayId: string): string {
  return `/v1/tenants/${currentTenantId()}/gateways/${gatewayId}`;
}

// listEdgeProfiles fetches every EdgeX profile cached for one gateway.
export async function listEdgeProfiles(gatewayId: string): Promise<EdgeProfile[]> {
  const { data } = await client.get<EdgeProfile[]>(`${gatewayBase(gatewayId)}/edge-profiles`);
  return data;
}

// getEdgeProfile fetches one cached profile with its raw resources.
export async function getEdgeProfile(
  gatewayId: string,
  name: string,
): Promise<EdgeProfileDetail> {
  const { data } = await client.get<EdgeProfileDetail>(
    `${gatewayBase(gatewayId)}/edge-profiles/${encodeURIComponent(name)}`,
  );
  return data;
}

// adoptEdgeProfile adopts one cached profile. Pass mode "update" to overwrite
// a same-name native profile, or target_name to adopt under another name.
export async function adoptEdgeProfile(
  gatewayId: string,
  name: string,
  input: AdoptEdgeProfileInput = {},
): Promise<AdoptEdgeProfileResult> {
  const { data } = await client.post<AdoptEdgeProfileResult>(
    `${gatewayBase(gatewayId)}/edge-profiles/${encodeURIComponent(name)}/adopt`,
    input,
  );
  return data;
}
