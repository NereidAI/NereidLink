// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// notifications.ts exposes the in-app notification inbox list and read
// state updates.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { Page } from "./devices";


// Notification is one inbox row.
export interface Notification {
  notification_id: string;
  tenant_id: string;
  alarm_id?: string;
  device_id?: string;
  severity: string;
  title: string;
  body?: string;
  state: "NEW" | "READ";
  created_at: string;
  read_at?: string;
}

// ListNotificationsParams carries the state filter and pagination.
export interface ListNotificationsParams {
  state?: string;
  page: number;
  page_size: number;
}

// listNotifications fetches one page of inbox notifications.
export async function listNotifications(
  params: ListNotificationsParams,
): Promise<Page<Notification>> {
  const { data } = await client.get<Page<Notification>>(
    `/v1/tenants/${currentTenantId()}/notifications`,
    { params },
  );
  return data;
}

// markNotificationsRead marks the supplied notifications as read.
export async function markNotificationsRead(ids: string[]): Promise<void> {
  await client.post(`/v1/tenants/${currentTenantId()}/notifications/read`, { ids });
}
