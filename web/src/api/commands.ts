// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// commands.ts exposes the tenant-wide command center list.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { CommandRecord, Page } from "./devices";


// ListCommandsParams carries command center filters and pagination.
export interface ListCommandsParams {
  device_id?: string;
  name?: string;
  state?: string;
  page: number;
  page_size: number;
}

// listCommands fetches one page of commands across all tenant devices.
export async function listCommands(
  params: ListCommandsParams,
): Promise<Page<CommandRecord>> {
  const { data } = await client.get<Page<CommandRecord>>(
    `/v1/tenants/${currentTenantId()}/commands`,
    { params },
  );
  return data;
}
