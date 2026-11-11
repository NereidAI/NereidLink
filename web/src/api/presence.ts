// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Presence API: the presence snapshot seeds the online map of a freshly
// loaded client, which cannot replay ONLINE events emitted before the
// WebSocket connected.

import client from "./client";
import { currentTenantId } from "./tenant";


// PresenceSnapshotDevice is one online device entry.
export interface PresenceSnapshotDevice {
  device_id: string;
  ingress_id: string;
  session_id: string;
  gateway_id?: string;
}

// PresenceSnapshot lists every online device of the tenant.
export interface PresenceSnapshot {
  devices: PresenceSnapshotDevice[];
}

// listPresence fetches the current presence snapshot.
export async function listPresence(): Promise<PresenceSnapshot> {
  const { data } = await client.get<PresenceSnapshot>(
    `/v1/tenants/${currentTenantId()}/presence`,
  );
  return data;
}
