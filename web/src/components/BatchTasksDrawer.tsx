// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BatchTasksDrawer shows asynchronous batch tasks, live aggregate progress
// and per-device outcomes.

import { useEffect, useMemo, useState } from "react";
import {
  Drawer,
  List,
  Progress,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  listBatchCommands,
  listBatchItems,
  type BatchTask,
  type BatchTaskItem,
} from "../api/batchCommands";
import { useWSStore } from "../store/wsStore";

// terminalTaskStates classify progress tag colors.
const failedTasks = new Set(["failed"]);
const successTasks = new Set(["completed"]);

// BatchTasksDrawerProps controls rendering.
interface BatchTasksDrawerProps {
  open: boolean;
  initialTaskId?: string;
  onClose: () => void;
}

// BatchTasksDrawer renders live task progress.
export default function BatchTasksDrawer({
  open,
  initialTaskId,
  onClose,
}: BatchTasksDrawerProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const events = useWSStore((state) => state.batchTaskEvents);
  const [selectedId, setSelectedId] = useState(initialTaskId ?? "");

  const tasksQuery = useQuery({
    queryKey: ["batch-commands"],
    queryFn: () => listBatchCommands(1, 100),
    enabled: open,
  });

  useEffect(() => {
    if (!open) return;
    subscribe("command-task-progress", "command_task", []);
    return () => unsubscribe("command-task-progress");
  }, [open, subscribe, unsubscribe]);

  useEffect(() => {
    if (events.length === 0) return;
    void queryClient.invalidateQueries({ queryKey: ["batch-commands"] });
  }, [events, queryClient]);

  const tasks = useMemo(() => {
    const map = new Map<string, BatchTask>();
    for (const task of tasksQuery.data?.items ?? []) map.set(task.id, task);
    for (const event of events) map.set(event.task.id, event.task);
    return [...map.values()].sort(
      (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
    );
  }, [events, tasksQuery.data]);

  const effectiveSelectedId = selectedId || tasks[0]?.id || "";

  useEffect(() => {
    if (effectiveSelectedId) {
      void queryClient.invalidateQueries({
        queryKey: ["batch-items", effectiveSelectedId],
      });
    }
  }, [effectiveSelectedId, events, queryClient]);

  const itemsQuery = useQuery({
    queryKey: ["batch-items", effectiveSelectedId],
    queryFn: () => listBatchItems(effectiveSelectedId),
    enabled: open && effectiveSelectedId !== "",
  });

  const taskTagColor = (state: string) => {
    if (failedTasks.has(state)) return "error";
    if (successTasks.has(state)) return "success";
    if (state === "partial") return "warning";
    return "processing";
  };

  const itemColumns = [
    {
      title: t("common.device"),
      dataIndex: "device_id",
      render: (id: string) => (
        <Typography.Text style={{ fontSize: 12 }}>{id.slice(0, 8)}</Typography.Text>
      ),
    },
    {
      title: t("common.state"),
      dataIndex: "state",
      render: (state: string) => <Tag>{state}</Tag>,
    },
    {
      title: t("common.error"),
      dataIndex: "error",
      render: (value?: string) => value ?? "",
    },
  ];

  return (
    <Drawer
      open={open}
      width={760}
      title={t("pages.commandCenter.batchTasks")}
      onClose={onClose}
    >
      <Space direction="vertical" size="middle" style={{ width: "100%" }}>
        <List
          bordered
          dataSource={tasks}
          loading={tasksQuery.isLoading}
          renderItem={(task) => {
            const processed = task.succeeded_count + task.failed_count;
            return (
              <List.Item
                onClick={() => setSelectedId(task.id)}
                style={{ cursor: "pointer", background: task.id === effectiveSelectedId ? "#f0f7ff" : undefined }}
              >
                <Space direction="vertical" style={{ width: "100%" }}>
                  <Space style={{ width: "100%", justifyContent: "space-between" }}>
                    <Typography.Text strong>{task.name}</Typography.Text>
                    <Tag color={taskTagColor(task.state)}>{task.state}</Tag>
                  </Space>
                  <Progress
                    percent={task.total_count === 0 ? 0 : Math.round((processed / task.total_count) * 100)}
                    size="small"
                    status={task.state === "failed" ? "exception" : "normal"}
                  />
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {task.succeeded_count}/{task.total_count} · {task.failed_count} failed
                  </Typography.Text>
                </Space>
              </List.Item>
            );
          }}
        />
        <Table<BatchTaskItem>
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
