// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// LinkHealthTab lists the gateway's link outage history (lease-detected
// and edge-reported), newest first with server-side pagination, source
// filter and an optional time window. It shows intervals, unlike the
// Alarms tab which shows alarm lifecycle events.

import { useMemo, useState } from "react";
import { Card, DatePicker, Space, Table, Tag } from "antd";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import dayjs, { type Dayjs } from "dayjs";

import FilterSearchBar from "../FilterSearchBar";
import { useServerTable } from "../../hooks/useServerTable";
import { formatDateTime } from "../../utils/format";
import {
  listLinkEpisodes,
  type LinkEpisodeRow,
  type LinkEpisodeQuery,
} from "../../api/linkEpisodes";

interface LinkHealthTabProps {
  // gatewayId is the detailed gateway device.
  gatewayId: string;
}

// formatDuration renders a millisecond interval compactly, mirroring the
// digest style (Xh YYm / YYm ZZs).
function formatDuration(ms: number): string {
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}h ${minutes}m`;
  if (minutes > 0) return `${minutes}m ${seconds}s`;
  return `${seconds}s`;
}

// LinkHealthTab renders the link episode history table.
export default function LinkHealthTab({ gatewayId }: LinkHealthTabProps) {
  const { t } = useTranslation();
  const [sourceFilter, setSourceFilter] = useState("");
  const [range, setRange] = useState<[Dayjs | null, Dayjs | null] | null>(null);

  const params = useMemo<Omit<LinkEpisodeQuery, "page" | "page_size">>(
    () => ({
      gateway_id: gatewayId,
      source: sourceFilter || undefined,
      start: range?.[0]?.toISOString(),
      end: range?.[1]?.toISOString(),
    }),
    [gatewayId, sourceFilter, range],
  );

  const table = useServerTable("link-episodes", params, listLinkEpisodes);

  const columns = [
    {
      title: t("pages.deviceDetail.linkHealth.startedAt"),
      dataIndex: "started_at",
      width: 200,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.deviceDetail.linkHealth.recoveredAt"),
      dataIndex: "ended_at",
      width: 200,
      render: (value?: string) =>
        value ? (
          formatDateTime(value)
        ) : (
          <Tag color="processing">{t("pages.deviceDetail.linkHealth.ongoing")}</Tag>
        ),
    },
    {
      title: t("pages.deviceDetail.linkHealth.duration"),
      width: 110,
      render: (_: unknown, row: LinkEpisodeRow) =>
        row.ended_at
          ? formatDuration(dayjs(row.ended_at).diff(dayjs(row.started_at)))
          : "-",
    },
    {
      title: t("pages.deviceDetail.linkHealth.source"),
      dataIndex: "source",
      width: 120,
      render: (source: string) => (
        <Tag color={source === "edge" ? "blue" : "default"}>
          {t(`pages.deviceDetail.linkHealth.source_${source}`)}
        </Tag>
      ),
    },
    {
      title: t("pages.deviceDetail.linkHealth.reason"),
      dataIndex: "down_reason",
      render: (value: string) => value || "-",
    },
    {
      title: t("pages.deviceDetail.linkHealth.alarm"),
      dataIndex: "alarm_id",
      width: 110,
      render: (alarmID?: string) =>
        alarmID ? (
          <Link to={`/devices/${gatewayId}?tab=alarms&alarm_id=${alarmID}`}>
            <Tag color="orange">{t("pages.deviceDetail.linkHealth.viewAlarm")}</Tag>
          </Link>
        ) : (
          "-"
        ),
    },
  ];

  return (
    <Card>
      <Space style={{ marginBottom: 12 }}>
        <DatePicker.RangePicker
          showTime
          value={range}
          onChange={(value) => setRange(value as [Dayjs | null, Dayjs | null] | null)}
        />
      </Space>
      <FilterSearchBar
        filters={[
          {
            key: "source",
            placeholder: t("pages.deviceDetail.linkHealth.source"),
            value: sourceFilter,
            onChange: setSourceFilter,
            options: [
              {
                value: "lease",
                label: t("pages.deviceDetail.linkHealth.source_lease"),
              },
              {
                value: "edge",
                label: t("pages.deviceDetail.linkHealth.source_edge"),
              },
            ],
          },
        ]}
      />
      <Table
        rowKey="episode_id"
        loading={table.loading}
        columns={columns}
        dataSource={table.items}
        pagination={table.pagination}
        onChange={table.onChange}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
    </Card>
  );
}
