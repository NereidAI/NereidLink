// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProbeTasksDrawer shows one probe task's aggregate progress and its
// per-device outcomes, refreshing live from the probe_task WebSocket
// resource.

import { useEffect, useMemo } from "react";
import {
  Button,
  Drawer,
  Progress,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  decodeProbeResult,
  listProbeItems,
  type ProbeTask,
  type ProbeTaskItem,
} from "../api/probes";
import { listDevices } from "../api/devices";
import DeviceNameCell from "./DeviceNameCell";
import { useWSStore } from "../store/wsStore";

// ProbeTasksDrawerProps controls rendering.
interface ProbeTasksDrawerProps {
  open: boolean;
  task: ProbeTask;
  onClose: () => void;
}

// ProbeTasksDrawer renders live per-device probe results.
export default function ProbeTasksDrawer({
  open,
  task,
  onClose,
}: ProbeTasksDrawerProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const events = useWSStore((state) => state.probeTaskEvents);

  const itemsQuery = useQuery({
    queryKey: ["probe-items", task.id],
    queryFn: () => listProbeItems(task.id),
    enabled: open,
  });

  // A probe task can span multiple gateways. Fetch one large device page
  // to map every item's device id to a readable name (with alias when
  // present) instead of showing opaque UUID fragments.
  const devicesQuery = useQuery({
    queryKey: ["devices-all-for-probe"],
    queryFn: () => listDevices({ page: 1, page_size: 500 }),
    enabled: open,
  });

  useEffect(() => {
    if (!open) return;
    subscribe("probe-task-progress", "probe_task", []);
    return () => unsubscribe("probe-task-progress");
  }, [open, subscribe, unsubscribe]);

  // Any progress frame for this task refreshes both the item table; the
  // parent page owns the aggregate task row refresh.
  useEffect(() => {
    if (events.some((event) => event.task.id === task.id)) {
      void queryClient.invalidateQueries({ queryKey: ["probe-items", task.id] });
    }
  }, [events, queryClient, task.id]);

  const resultCells = useMemo(() => {
    const cells = new Map<string, { reachable?: boolean; rttMS?: number }>();
    for (const item of itemsQuery.data?.items ?? []) {
      const decoded = decodeProbeResult(item) as
        | { reachable?: boolean; rtt_ms?: number }
        | undefined;
      cells.set(item.device_id, {
        reachable: decoded?.reachable,
        rttMS: decoded?.rtt_ms,
      });
    }
    return cells;
  }, [itemsQuery.data]);

  const deviceRecords = useMemo(() => {
    const map = new Map<string, { name: string; alias?: string }>();
    for (const device of devicesQuery.data?.items ?? []) {
      map.set(device.id, { name: device.canonical_name, alias: device.alias });
    }
    return map;
  }, [devicesQuery.data]);

  const itemColumns = [
    {
      title: t("common.device"),
      dataIndex: "device_id",
      render: (id: string) => {
        const named = deviceRecords.get(id);
        return (
          <DeviceNameCell
            id={id}
            canonicalName={named?.name ?? ""}
            alias={named?.alias}
          />
        );
      },
    },
    {
      title: t("common.state"),
      dataIndex: "state",
      width: 120,
      render: (state: string) => {
        if (state === "succeeded") return <Tag color="success">{t("status.succeeded")}</Tag>;
        if (state === "failed") return <Tag color="error">{t("status.failed")}</Tag>;
        return <Tag color="processing">{t("status.running")}</Tag>;
      },
    },
    {
      title: t("pages.probes.result"),
      dataIndex: "device_id",
      render: (id: string) => {
        const cell = resultCells.get(id);
        if (cell?.reachable === true) {
          return (
            <Space size={4}>
              <Tag color="success">{t("pages.probes.reachable")}</Tag>
              {cell.rttMS !== undefined ? (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t("pages.probes.rttMS", { ms: cell.rttMS })}
                </Typography.Text>
              ) : null}
            </Space>
          );
        }
        if (cell?.reachable === false) return <Tag color="error">{t("pages.probes.unreachable")}</Tag>;
        return <Typography.Text type="secondary">-</Typography.Text>;
      },
    },
    {
      title: t("common.error"),
      dataIndex: "error",
      render: (value?: string) =>
        value ? (
          <Typography.Text type="danger" style={{ fontSize: 12 }}>
            {value}
          </Typography.Text>
        ) : (
          ""
        ),
    },
  ];

  const processed = task.succeeded_count + task.failed_count;
  const inFlight = task.state === "pending" || task.state === "running";

  return (
    <Drawer
      open={open}
      width={760}
      title={t("pages.probes.tasksTitle")}
      onClose={onClose}
      extra={
        <Button onClick={onClose}>{t("common.close")}</Button>
      }
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <div>
          <Space style={{ justifyContent: "space-between", width: "100%" }}>
            <Space>
              <Typography.Text strong>
                {t("pages.probes.modeLabel", { mode: task.mode })}
              </Typography.Text>
              <Tag color={inFlight ? "processing" : task.state === "completed" ? "success" : task.state === "partial" ? "warning" : "error"}>
                {t(`pages.probes.states.${task.state}`)}
              </Tag>
            </Space>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {task.succeeded_count}/{task.total_count} · {task.failed_count} {t("pages.probes.failed")}
            </Typography.Text>
          </Space>
          <Progress
            percent={task.total_count === 0 ? 0 : Math.round((processed / task.total_count) * 100)}
            size="small"
            status={task.state === "failed" ? "exception" : task.state === "completed" ? "success" : "active"}
          />
        </div>
        <Table<ProbeTaskItem>
          rowKey="id"
          size="small"
          pagination={false}
          loading={itemsQuery.isLoading}
          dataSource={itemsQuery.data?.items ?? []}
          columns={itemColumns}
        />
      </Space>
    </Drawer>
  );
}
