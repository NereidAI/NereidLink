// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OtaPage lists firmware rollout tasks with aggregate progress and opens a
// live per-device progress drawer. Creation and cancellation are the
// mutating controls.

import { useState } from "react";
import {
  Button,
  Card,
  Progress,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import OtaCreateDrawer from "../components/OtaCreateDrawer";
import OtaTaskDrawer from "../components/OtaTaskDrawer";
import { useServerTable } from "../hooks/useServerTable";
import { listOtaTasks, type OtaTask } from "../api/ota";
import { formatDateTime } from "../utils/format";

// taskTagColor maps a rollout aggregate state to a tag color.
const taskTagColor = (state: string): string => {
  switch (state) {
    case "completed":
      return "success";
    case "failed":
    case "aborted":
      return "error";
    case "paused":
      return "warning";
    case "partial":
      return "orange";
    case "canceled":
      return "default";
    default:
      return "processing";
  }
};

// OtaPage renders the rollout task list.
export default function OtaPage() {
  const { t } = useTranslation();
  const [createOpen, setCreateOpen] = useState(false);
  const [stateFilter, setStateFilter] = useState("");
  const [selectedId, setSelectedId] = useState("");

  const { items, loading, pagination, onChange } = useServerTable(
    "ota-tasks",
    { state: stateFilter },
    (params) =>
      listOtaTasks({
        state: params.state || undefined,
        page: params.page,
        page_size: params.page_size,
      }),
  );

  const columns = [
    {
      title: t("pages.ota.version"),
      dataIndex: "firmware_version",
      render: (version: string, record: OtaTask) => (
        <Space size={6}>
          <Typography.Text strong>{version}</Typography.Text>
          <Tag color={record.firmware_id ? "geekblue" : "default"}>
            {record.firmware_id
              ? t("pages.ota.hosted")
              : t("pages.ota.external")}
          </Tag>
          {record.target_mode === "selector" && (
            <Tag color="purple">{t("pages.ota.targetSelectorShort")}</Tag>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.ota.state"),
      dataIndex: "state",
      width: 120,
      render: (state: string) => (
        <Tag color={taskTagColor(state)}>{t(`pages.ota.states.${state}`)}</Tag>
      ),
    },
    {
      title: t("pages.ota.progress"),
      key: "progress",
      width: 220,
      render: (_: unknown, record: OtaTask) => {
        const processed =
          record.succeeded_count +
          record.failed_count +
          record.canceled_count +
          record.skipped_count;
        const percent =
          record.total_count > 0
            ? Math.round((processed / record.total_count) * 100)
            : 0;
        return (
          <Progress
            percent={percent}
            size="small"
            status={record.state === "failed" ? "exception" : "normal"}
          />
        );
      },
    },
    {
      title: t("pages.ota.total"),
      dataIndex: "total_count",
      width: 90,
    },
    {
      title: t("pages.ota.succeeded"),
      dataIndex: "succeeded_count",
      width: 100,
      render: (value: number) => <Typography.Text type="success">{value}</Typography.Text>,
    },
    {
      title: t("pages.ota.failed"),
      dataIndex: "failed_count",
      width: 90,
      render: (value: number) =>
        value > 0 ? <Typography.Text type="danger">{value}</Typography.Text> : 0,
    },
    {
      title: t("pages.ota.createdAt"),
      dataIndex: "created_at",
      width: 170,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.ota.viewItems"),
      key: "items",
      width: 110,
      render: (_: unknown, record: OtaTask) => (
        <Button
          type="link"
          size="small"
          onClick={() => setSelectedId(record.id)}
        >
          {t("pages.ota.viewItems")}
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.ota.title")}
        subtitle={t("pages.ota.subtitle")}
        helpId="ota-tasks"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setCreateOpen(true)}
          >
            {t("pages.ota.create")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          filters={[
            {
              key: "state",
              placeholder: t("pages.ota.filters.stateAll"),
              value: stateFilter,
              onChange: setStateFilter,
              options: [
                "pending",
                "running",
                "paused",
                "completed",
                "partial",
                "failed",
                "aborted",
                "canceled",
              ].map((state) => ({
                value: state,
                label: t(`pages.ota.states.${state}`),
              })),
            },
          ]}
        />
        <Table<OtaTask>
          rowKey="id"
          loading={loading}
          columns={columns}
          dataSource={items}
          pagination={pagination}
          onChange={onChange}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <OtaCreateDrawer open={createOpen} onClose={() => setCreateOpen(false)} />
      <OtaTaskDrawer
        open={selectedId !== ""}
        taskId={selectedId}
        onClose={() => setSelectedId("")}
      />
    </>
  );
}
