// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProbesPage is the on-demand probe workbench: it creates probe tasks
// against gateway-bound sub-devices, lists tasks with server-side
// pagination and opens a live drawer of per-device outcomes. Progress is
// pushed by the server on the probe_task WebSocket resource; the page
// never polls.

import { useEffect, useMemo, useState } from "react";
import { Button, Card, Progress, Table, Tag, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import DeviceNameCell from "../components/DeviceNameCell";
import ProbeCreateDrawer from "../components/ProbeCreateDrawer";
import ProbeTasksDrawer from "../components/ProbeTasksDrawer";
import { listProbes, type ProbeTask } from "../api/probes";
import { listDevices } from "../api/devices";
import { useServerTable } from "../hooks/useServerTable";
import { useWSStore } from "../store/wsStore";
import { useDebounce } from "../hooks/useDebounce";
import { formatDateTime } from "../utils/format";

// ProbesPage renders the probe workbench.
export default function ProbesPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [selected, setSelected] = useState<ProbeTask | undefined>();
  const [stateFilter, setStateFilter] = useState("");
  const [modeFilter, setModeFilter] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const query = useDebounce(queryInput, 300);

  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const events = useWSStore((state) => state.probeTaskEvents);

  const params = useMemo(
    () => ({
      state: stateFilter || undefined,
      mode: modeFilter || undefined,
      q: query || undefined,
    }),
    [stateFilter, modeFilter, query],
  );
  const table = useServerTable("probes", params, (params) =>
    listProbes(
      { state: params.state, mode: params.mode, q: params.q },
      params.page,
      params.page_size,
    ),
  );

  // Gateway ids are stored on the task; map them to full records for the
  // combined canonical-name/alias gateway cell.
  const devicesQuery = useQuery({
    queryKey: ["devices-all-for-probe-page"],
    queryFn: () => listDevices({ page: 1, page_size: 500 }),
  });
  const deviceRecords = new Map(
    (devicesQuery.data?.items ?? []).map((device) => [device.id, device]),
  );

  // Filter dropdown options.
  const stateOptions = [
    { value: "pending", label: t("pages.probes.states.pending") },
    { value: "running", label: t("pages.probes.states.running") },
    { value: "completed", label: t("pages.probes.states.completed") },
    { value: "partial", label: t("pages.probes.states.partial") },
    { value: "failed", label: t("pages.probes.states.failed") },
  ];
  const modeOptions = [
    { value: "cached", label: t("pages.probes.modeCached") },
    { value: "fresh", label: t("pages.probes.modeFresh") },
    { value: "strict", label: t("pages.probes.modeStrict") },
  ];

  useEffect(() => {
    subscribe("probe-task-list", "probe_task", []);
    return () => unsubscribe("probe-task-list");
  }, [subscribe, unsubscribe]);

  // Live progress invalidates the task list. The open drawer's aggregate
  // task is derived during render (liveSelected) rather than patched via
  // setState, avoiding a cascading render.
  useEffect(() => {
    if (events.length === 0) return;
    void queryClient.invalidateQueries({ queryKey: ["probes"] });
  }, [events, queryClient]);

  // liveSelected overlays the newest progress frame for the open task on
  // its snapshot captured when the drawer was opened.
  const liveSelected = useMemo(() => {
    if (!selected) return undefined;
    const latest = [...events].reverse().find((event) => event.task.id === selected.id);
    return latest ? latest.task : selected;
  }, [selected, events]);

  const taskStateTag = (state: ProbeTask["state"]) => {
    if (state === "completed") return <Tag color="success">{t("pages.probes.states.completed")}</Tag>;
    if (state === "partial") return <Tag color="warning">{t("pages.probes.states.partial")}</Tag>;
    if (state === "failed") return <Tag color="error">{t("pages.probes.states.failed")}</Tag>;
    return <Tag color="processing">{t("pages.probes.states.running")}</Tag>;
  };

  const columns = [
    {
      title: t("common.created"),
      dataIndex: "created_at",
      width: 190,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.probes.gateway"),
      dataIndex: "gateway_id",
      render: (id: string) => {
        if (!id) return "-";
        const device = deviceRecords.get(id);
        return (
          <DeviceNameCell
            id={id}
            canonicalName={device?.canonical_name ?? ""}
            alias={device?.alias}
          />
        );
      },
    },
    {
      title: t("pages.probes.mode"),
      dataIndex: "mode",
      width: 100,
      render: (value: string) => <Typography.Text type="secondary">{value}</Typography.Text>,
    },
    {
      title: t("pages.probes.timeoutMS"),
      dataIndex: "timeout_ms",
      width: 130,
      render: (value: number) => (
        <Typography.Text type="secondary">{t("pages.probes.rttMS", { ms: value })}</Typography.Text>
      ),
    },
    {
      title: t("pages.probes.progress"),
      dataIndex: "total_count",
      render: (_: number, task: ProbeTask) => {
        const processed = task.succeeded_count + task.failed_count;
        return (
          <Progress
            percent={task.total_count === 0 ? 0 : Math.round((processed / task.total_count) * 100)}
            size="small"
            style={{ width: 160 }}
            status={task.state === "failed" ? "exception" : task.state === "completed" ? "success" : "active"}
          />
        );
      },
    },
    {
      title: t("pages.probes.counts"),
      width: 140,
      render: (_: unknown, task: ProbeTask) => (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {task.succeeded_count}/{task.total_count} · {task.failed_count} {t("pages.probes.failed")}
        </Typography.Text>
      ),
    },
    {
      title: t("common.state"),
      dataIndex: "state",
      width: 120,
      render: (state: ProbeTask["state"]) => taskStateTag(state),
    },
    {
      title: t("common.actions"),
      width: 100,
      render: (_: unknown, task: ProbeTask) => (
        <Button type="link" size="small" onClick={() => setSelected(task)}>
          {t("pages.probes.view")}
        </Button>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.probes.title")}
        subtitle={t("pages.probes.subtitle")}
        helpId="probes"
        extra={
          <Button type="primary" onClick={() => setCreateOpen(true)}>
            {t("pages.probes.create")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          filters={[
            {
              key: "state",
              placeholder: t("common.state"),
              value: stateFilter,
              onChange: setStateFilter,
              options: stateOptions,
            },
            {
              key: "mode",
              placeholder: t("pages.probes.mode"),
              value: modeFilter,
              onChange: setModeFilter,
              options: modeOptions,
            },
          ]}
          searchValue={queryInput}
          searchPlaceholder={t("pages.probes.searchPlaceholder")}
          onSearchChange={setQueryInput}
        />
        <Table<ProbeTask>
          rowKey="id"
          loading={table.loading}
          columns={columns}
          dataSource={table.items}
          pagination={table.pagination}
          onRow={(task) => ({ onClick: () => setSelected(task) })}
          onChange={(pagination) =>
            table.onChange({
              current: pagination.current,
              pageSize: pagination.pageSize,
            })
          }
        />
      </Card>
      <ProbeCreateDrawer
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(task) => {
          setCreateOpen(false);
          setSelected(task);
        }}
      />
      {liveSelected ? (
        <ProbeTasksDrawer
          open
          task={liveSelected}
          onClose={() => setSelected(undefined)}
        />
      ) : null}
    </>
  );
}
