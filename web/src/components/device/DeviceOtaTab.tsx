// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceOtaTab shows one directly connected device's firmware rollout
// history (per-device items joined to each parent task) and starts a
// single-device rollout. Direct devices poll an authenticated endpoint and
// download through the relay rather than receiving a gateway frame.

import { useState } from "react";
import {
  Button,
  Card,
  Drawer,
  Progress,
  Space,
  Table,
  Tag,
  Timeline,
  Typography,
} from "antd";
import { EyeOutlined, PlusOutlined } from "@ant-design/icons";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import OtaCreateDrawer from "../OtaCreateDrawer";
import { useServerTable } from "../../hooks/useServerTable";
import {
  listDeviceOtaItems,
  listOtaEvents,
  type DeviceOtaItem,
  type OtaTimelineEvent,
} from "../../api/ota";
import { formatDateTime } from "../../utils/format";

// phaseColor maps an item phase to a tag/timeline color.
function phaseColor(phase: string): string {
  switch (phase) {
    case "succeeded":
    case "confirmed":
      return "success";
    case "failed":
      return "error";
    case "canceled":
    case "skipped":
      return "default";
    default:
      return "processing";
  }
}

// taskTagColor maps the parent task aggregate state to a tag color.
function taskTagColor(state: string): string {
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
}

// DeviceOtaTab renders the rollout history and launch control.
export default function DeviceOtaTab({ deviceId }: { deviceId: string }) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [timelineTask, setTimelineTask] = useState<DeviceOtaItem | null>(null);

  const { items, loading, pagination, onChange } = useServerTable(
    "device-ota-items",
    { deviceId },
    (params) =>
      listDeviceOtaItems(params.deviceId, {
        page: params.page,
        page_size: params.page_size,
      }),
  );

  const columns = [
    {
      title: t("pages.ota.version"),
      dataIndex: "firmware_version",
      render: (version: string) => (
        <Typography.Text strong>{version || "-"}</Typography.Text>
      ),
    },
    {
      title: t("pages.ota.state"),
      dataIndex: "state",
      width: 150,
      render: (phase: string, record: DeviceOtaItem) => (
        <Space direction="vertical" size={0}>
          <Tag color={phaseColor(phase)} style={{ marginInlineEnd: 0 }}>
            {t(`pages.ota.itemStates.${phase}`)}
          </Tag>
          {record.result_code && record.result_code !== "none" && (
            <Typography.Text type="warning" style={{ fontSize: 12 }}>
              {t(`pages.ota.resultCodes.${record.result_code}`, record.result_code)}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.ota.progress"),
      dataIndex: "progress_pct",
      width: 180,
      render: (value: number, record: DeviceOtaItem) =>
        record.state === "succeeded" ? (
          <Progress percent={100} size="small" />
        ) : (
          <Progress
            percent={value}
            size="small"
            status={record.state === "failed" ? "exception" : "active"}
          />
        ),
    },
    {
      title: t("pages.deviceOta.taskState"),
      dataIndex: "task_state",
      width: 120,
      render: (state: string) => (
        <Tag color={taskTagColor(state)}>{t(`pages.ota.states.${state}`)}</Tag>
      ),
    },
    {
      title: t("pages.ota.createdAt"),
      dataIndex: "created_at",
      width: 170,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 90,
      render: (_: unknown, record: DeviceOtaItem) => (
        <Button
          type="link"
          size="small"
          icon={<EyeOutlined />}
          onClick={() => setTimelineTask(record)}
        >
          {t("pages.deviceOta.timeline")}
        </Button>
      ),
    },
  ];

  return (
    <>
      <Card
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setCreateOpen(true)}
          >
            {t("pages.deviceOta.create")}
          </Button>
        }
      >
        <Table<DeviceOtaItem>
          rowKey="id"
          loading={loading}
          columns={columns}
          dataSource={items}
          pagination={pagination}
          onChange={onChange}
          locale={{ emptyText: t("pages.deviceOta.empty") }}
        />
      </Card>
      <OtaCreateDrawer
        open={createOpen}
        presetDevices={[deviceId]}
        lockTargets
        onClose={() => {
          setCreateOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["device-ota-items"] });
        }}
      />
      <TimelineDrawer
        item={timelineTask}
        deviceId={deviceId}
        onClose={() => setTimelineTask(null)}
      />
    </>
  );
}

// TimelineDrawer shows the append-only phase timeline of one rollout row.
function TimelineDrawer({
  item,
  deviceId,
  onClose,
}: {
  item: DeviceOtaItem | null;
  deviceId: string;
  onClose: () => void;
}) {
  const { t } = useTranslation();
  const taskId = item?.task_id ?? "";
  const eventsQuery = useQuery({
    queryKey: ["ota-events", taskId, deviceId],
    queryFn: () => listOtaEvents(taskId, deviceId),
    enabled: item !== null,
  });
  const events: OtaTimelineEvent[] = eventsQuery.data ?? [];

  return (
    <Drawer
      open={item !== null}
      onClose={onClose}
      width={560}
      title={
        item
          ? t("pages.deviceOta.timelineTitle", { version: item.firmware_version })
          : ""
      }
      destroyOnHidden
    >
      <Timeline
        items={events.map((ev) => ({
          color: phaseColor(ev.phase),
          children: (
            <Space direction="vertical" size={0}>
              <Space size={6}>
                <Tag color={phaseColor(ev.phase)} style={{ marginInlineEnd: 0 }}>
                  {t(`pages.ota.itemStates.${ev.phase}`)}
                </Tag>
                {ev.result_code && ev.result_code !== "none" && (
                  <Typography.Text type="warning" style={{ fontSize: 12 }}>
                    {t(`pages.ota.resultCodes.${ev.result_code}`, ev.result_code)}
                  </Typography.Text>
                )}
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t(`pages.ota.sources.${ev.source}`)}
                </Typography.Text>
              </Space>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {formatDateTime(ev.created_at)}
                {ev.message ? ` · ${ev.message}` : ""}
              </Typography.Text>
            </Space>
          ),
        }))}
      />
    </Drawer>
  );
}
