// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// linkEpisodes.ts exposes the read-only gateway link episode history
// used by the gateway detail Link Health tab.

import client from "./client";
import { currentTenantId } from "./tenant";
import type { PageEnvelope } from "../hooks/useServerTable";

// EpisodeSource values recorded on each episode row.
export type EpisodeSource = "lease" | "edge";

// LinkEpisodeRow is one gateway link outage interval.
export interface LinkEpisodeRow {
  episode_id: string;
  gateway_id: string;
  gateway_name: string;
  source: EpisodeSource;
  started_at: string;
  ended_at?: string;
  down_reason: string;
  edge_down_at?: string;
  alarm_id?: string;
}

// LinkEpisodeQuery are the list query parameters; empty values disable
// the filter.
export interface LinkEpisodeQuery {
  gateway_id?: string;
  source?: string;
  start?: string;
  end?: string;
  page: number;
  page_size: number;
}

// listLinkEpisodes returns one page of link episode rows, newest first.
export async function listLinkEpisodes(
  params: LinkEpisodeQuery,
): Promise<PageEnvelope<LinkEpisodeRow>> {
  const search = new URLSearchParams();
  search.set("page", String(params.page));
  search.set("page_size", String(params.page_size));
  if (params.gateway_id) search.set("gateway_id", params.gateway_id);
  if (params.source) search.set("source", params.source);
  if (params.start) search.set("start", params.start);
  if (params.end) search.set("end", params.end);
  const { data } = await client.get<PageEnvelope<LinkEpisodeRow>>(
    `/v1/tenants/${currentTenantId()}/link-episodes?${search.toString()}`,
  );
  return data;
}
