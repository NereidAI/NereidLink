// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// alarms.ts exposes the paged alarm center list, acknowledgment, assignment
// and comments.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { AlarmRow, Page } from "./devices";


// ListAlarmsParams carries alarm center filters and pagination.
export interface ListAlarmsParams {
  device_id?: string;
  alarm_type?: string;
  assignee?: string;
  state?: string;
  severity?: string;
  page: number;
  page_size: number;
}

// listAlarms fetches one page of alarm audit rows.
export async function listAlarms(
  params: ListAlarmsParams,
): Promise<Page<AlarmRow>> {
  const { data } = await client.get<Page<AlarmRow>>(
    `/v1/tenants/${currentTenantId()}/alarms`,
    { params },
  );
  return data;
}

// ackAlarm acknowledges one ACTIVE alarm.
export async function ackAlarm(alarmID: string): Promise<void> {
  await client.post(
    `/v1/tenants/${currentTenantId()}/alarms/${alarmID}/ack`,
  );
}

// AlarmComment is one entry in the alarm timeline.
export interface AlarmComment {
  comment_id: string;
  alarm_id: string;
  author_id: string;
  author_name: string;
  body: string;
  created_at: string;
}

// assignAlarm sets or clears the alarm assignee.
export async function assignAlarm(
  alarmID: string,
  assigneeID: string,
): Promise<void> {
  await client.post(
    `/v1/tenants/${currentTenantId()}/alarms/${alarmID}/assign`,
    { assignee_id: assigneeID },
  );
}

// listAlarmComments returns the alarm comment timeline.
export async function listAlarmComments(
  alarmID: string,
): Promise<AlarmComment[]> {
  const { data } = await client.get<AlarmComment[]>(
    `/v1/tenants/${currentTenantId()}/alarms/${alarmID}/comments`,
  );
  return data;
}

// addAlarmComment appends a text comment to an alarm.
export async function addAlarmComment(
  alarmID: string,
  body: string,
): Promise<AlarmComment> {
  const { data } = await client.post<AlarmComment>(
    `/v1/tenants/${currentTenantId()}/alarms/${alarmID}/comments`,
    { body },
  );
  return data;
}
