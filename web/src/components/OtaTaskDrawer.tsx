// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OtaTaskDrawer shows one rollout's live aggregate and its per-device items.
// It subscribes to the ota_task WebSocket resource while open and invalidates
// the task/items/events queries on every pushed frame; it never polls. Each
// item row expands to its append-only phase timeline. A safe cancel action
// stops targets that have not begun applying.

import { useEffect } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Drawer,
  Popconfirm,
  Progress,
  Space,
  Table,
  Tag,
  Timeline,
  Tooltip,
  Typography,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  cancelOtaTask,
  getOtaTask,
  listOtaEvents,
  listOtaItems,
  pauseOtaTask,
  resumeOtaTask,
  type OtaItem,
  type OtaTask,
  type OtaTimelineEvent,
} from "../api/ota";
import { listDevices } from "../api/devices";
import { useWSStore } from "../store/wsStore";
import { extractError } from "../api/client";
import { formatDateTime } from "../utils/format";

interface OtaTaskDrawerProps {
  open: boolean;
  taskId: string;
  onClose: () => void;
}

// terminalStates are the phases that close the rollout for one device.
const terminalStates = new Set(["succeeded", "failed", "canceled", "skipped"]);

// taskStateColor maps an aggregate task state to a tag color.
const taskStateColor = (state: string): string => {
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

// itemTagColor maps a per-device state to a tag color.
const itemTagColor = (state: string): string => {
  switch (state) {
    case "succeeded":
      return "success";
    case "failed":
      return "error";
    case "skipped":
      return "default";
    case "canceled":
      return "warning";
    default:
      return "processing";
  }
};

// timelineColor maps a timeline phase to an AntD Timeline dot color.
const timelineColor = (ev: OtaTimelineEvent): string => {
  if (ev.phase === "succeeded" || ev.phase === "confirmed") return "green";
  if (ev.phase === "failed") return "red";
  if (ev.phase === "canceled" || ev.phase === "skipped") return "gray";
  return "blue";
};

// DeviceTimeline renders one device's append-only phase history.
function DeviceTimeline({ taskId, deviceId }: { taskId: string; deviceId: string }) {
  const { t } = useTranslation();
  const eventsQuery = useQuery({
    queryKey: ["ota-events", taskId, deviceId],
    queryFn: () => listOtaEvents(taskId, deviceId),
    enabled: taskId !== "" && deviceId !== "",
  });
  const events = eventsQuery.data ?? [];
  return (
    <Timeline
      items={events.map((ev) => ({
        color: timelineColor(ev),
        children: (
          <Space direction="vertical" size={0}>
            <Space size={6}>
              <Tag color={timelineColor(ev)} style={{ marginInlineEnd: 0 }}>
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
  );
}

// OtaTaskDrawer renders live device progress for one rollout.
export default function OtaTaskDrawer({
  open,
  taskId,
  onClose,
}: OtaTaskDrawerProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const events = useWSStore((state) => state.otaTaskEvents);

  useEffect(() => {
    if (!open) return;
    subscribe("ota-task-progress", "ota_task", []);
    return () => unsubscribe("ota-task-progress");
  }, [open, subscribe, unsubscribe]);

  useEffect(() => {
    if (!open || events.length === 0) return;
    void queryClient.invalidateQueries({ queryKey: ["ota-task", taskId] });
    void queryClient.invalidateQueries({ queryKey: ["ota-items", taskId] });
    void queryClient.invalidateQueries({ queryKey: ["ota-events", taskId] });
  }, [events, open, queryClient, taskId]);

  const taskQuery = useQuery<OtaTask>({
    queryKey: ["ota-task", taskId],
    queryFn: () => getOtaTask(taskId),
    enabled: open && taskId !== "",
  });

  const itemsQuery = useQuery({
    queryKey: ["ota-items", taskId],
    queryFn: () =>
      listOtaItems(taskId, { page: 1, page_size: 500 }).then(
        (page) => page.items,
      ),
    enabled: open && taskId !== "",
  });

  const devicesQuery = useQuery({
    queryKey: ["devices", "ota-labels"],
    queryFn: () =>
      listDevices({ state: "ACTIVE", page: 1, page_size: 500 }).then(
        (page) => page.items,
      ),
    enabled: open,
  });
  const nameOf = (id: string) =>
    devicesQuery.data?.find((device) => device.id === id)?.canonical_name ??
    id.slice(0, 8);

  const task = taskQuery.data;
  const terminalCount = task
    ? task.succeeded_count +
      task.failed_count +
      task.canceled_count +
      task.skipped_count
    : 0;
  const percent =
    task && task.total_count > 0
      ? Math.round((terminalCount / task.total_count) * 100)
      : 0;
  const cancellable =
    task?.state === "pending" || task?.state === "running";

  const cancelMutation = useMutation({
    mutationFn: () => cancelOtaTask(taskId),
    onSuccess: async (res) => {
      await queryClient.invalidateQueries({ queryKey: ["ota-tasks"] });
      await queryClient.invalidateQueries({ queryKey: ["ota-task", taskId] });
      await queryClient.invalidateQueries({ queryKey: ["ota-items", taskId] });
      if (res.still_applying_count > 0) {
        message.warning(
          t("pages.ota.toasts.canceledApplying", {
            count: res.still_applying_count,
          }),
        );
      } else {
        message.success(t("pages.ota.toasts.canceled"));
      }
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const refreshTask = async () => {
    await queryClient.invalidateQueries({ queryKey: ["ota-tasks"] });
    await queryClient.invalidateQueries({ queryKey: ["ota-task", taskId] });
    await queryClient.invalidateQueries({ queryKey: ["ota-items", taskId] });
  };

  const pauseMutation = useMutation({
    mutationFn: () => pauseOtaTask(taskId),
    onSuccess: async () => {
      await refreshTask();
      message.success(t("pages.ota.toasts.paused"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const resumeMutation = useMutation({
    mutationFn: () => resumeOtaTask(taskId),
    onSuccess: async () => {
      await refreshTask();
      message.success(t("pages.ota.toasts.resumed"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // Batching is active when the policy opens a fraction/sized slice; legacy
  // single-batch tasks have both fields unset.
  const batched =
    task?.policy.batch_percent != null || task?.policy.batch_size != null;

  const columns = [
    {
      title: t("pages.ota.drawer.device"),
      dataIndex: "device_id",
      render: (id: string) => (
        <Typography.Text style={{ fontSize: 12 }}>{nameOf(id)}</Typography.Text>
      ),
    },
    {
      title: t("pages.ota.drawer.state"),
      dataIndex: "state",
      width: 120,
      render: (state: string, record: OtaItem) => (
        <Space size={4} direction="vertical">
          <Tag color={itemTagColor(state)}>
            {t(`pages.ota.itemStates.${state}`)}
          </Tag>
          {record.result_code && record.result_code !== "none" && (
            <Typography.Text type="warning" style={{ fontSize: 11 }}>
              {t(`pages.ota.resultCodes.${record.result_code}`, record.result_code)}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.ota.drawer.progress"),
      dataIndex: "progress_pct",
      width: 160,
      render: (value: number, record: OtaItem) =>
        terminalStates.has(record.state) ? (
          <Progress
            percent={record.state === "succeeded" ? 100 : value}
            size="small"
            status={
              record.state === "succeeded"
                ? "success"
                : record.state === "canceled" || record.state === "skipped"
                  ? "normal"
                  : "exception"
            }
          />
        ) : (
          <Progress percent={value} size="small" />
        ),
    },
    {
      title: t("pages.ota.drawer.message"),
      dataIndex: "message",
      ellipsis: true,
      render: (value?: string) => value ?? "",
    },
    ...(batched
      ? [
          {
            title: t("pages.ota.drawer.batch"),
            dataIndex: "batch_no",
            width: 80,
            render: (no?: number) => (
              <Tag>{(no ?? 0) > 0 ? no : "—"}</Tag>
            ),
          },
        ]
      : []),
  ];

  return (
    <Drawer
      open={open}
      width={820}
      title={t("pages.ota.drawer.title")}
      onClose={onClose}
      extra={
        task && (cancellable || task.state === "paused") ? (
          <Space>
            {task.state === "paused" ? (
              <Button
                type="primary"
                loading={resumeMutation.isPending}
                onClick={() => resumeMutation.mutate()}
              >
                {t("pages.ota.resume")}
              </Button>
            ) : (
              <Button
                loading={pauseMutation.isPending}
                onClick={() => pauseMutation.mutate()}
              >
                {t("pages.ota.pause")}
              </Button>
            )}
            <Popconfirm
              title={t("pages.ota.cancelConfirm.title")}
              description={t("pages.ota.cancelConfirm.description")}
              okText={t("pages.ota.cancelConfirm.ok")}
              cancelText={t("common.cancel")}
              okButtonProps={{ danger: true }}
              disabled={!cancellable}
              onConfirm={() => cancelMutation.mutate()}
            >
              <Button danger disabled={!cancellable}>
                {t("pages.ota.cancel")}
              </Button>
            </Popconfirm>
          </Space>
        ) : undefined
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        {task && (
          <div>
            <Space style={{ justifyContent: "space-between", width: "100%" }}>
              <Space size={8}>
                <Typography.Text strong>
                  {t("pages.ota.version")}: {task.firmware_version}
                </Typography.Text>
                <Tooltip
                  title={t(
                    `pages.ota.policies.${task.success_policy}`,
                    task.success_policy,
                  )}
                >
                  <Tag>
                    {t(`pages.ota.policies.${task.success_policy}`, task.success_policy)}
                  </Tag>
                </Tooltip>
                {task.target_mode === "selector" && (
                  <Tooltip title={t("pages.ota.selectorBadgeHint")}>
                    <Tag color="geekblue">
                      {t("pages.ota.targetSelectorShort")}
                    </Tag>
                  </Tooltip>
                )}
              </Space>
              <Tag color={taskStateColor(task.state)}>
                {t(`pages.ota.states.${task.state}`, task.state)}
              </Tag>
            </Space>
            {task.abort_reason && (
              <Alert
                type="error"
                showIcon
                style={{ marginTop: 8 }}
                message={task.abort_reason}
              />
            )}
            <Progress
              percent={percent}
              status={
                task.state === "failed" || task.state === "aborted"
                  ? "exception"
                  : task.state === "completed"
                    ? "success"
                    : "normal"
              }
            />
            <Space size={12} wrap>
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {task.succeeded_count}/{task.total_count} · {task.failed_count}{" "}
                {t("pages.ota.failed")} · {task.canceled_count}{" "}
                {t("pages.ota.canceled")} · {task.skipped_count}{" "}
                {t("pages.ota.skipped")}
              </Typography.Text>
              {batched && (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t("pages.ota.batchLabel", {
                    open: Math.max(task.batch_no, 0),
                  })}
                  {task.next_batch_at
                    ? ` · ${t("pages.ota.nextBatchAt", {
                        time: formatDateTime(task.next_batch_at),
                      })}`
                    : ""}
                </Typography.Text>
              )}
            </Space>
          </div>
        )}
        <Table<OtaItem>
          rowKey="id"
          size="small"
          pagination={false}
          loading={itemsQuery.isLoading}
          dataSource={itemsQuery.data ?? []}
          columns={columns}
          expandable={{
            expandedRowRender: (record) => (
              <DeviceTimeline taskId={taskId} deviceId={record.device_id} />
            ),
            rowExpandable: () => true,
          }}
        />
      </Space>
    </Drawer>
  );
}
