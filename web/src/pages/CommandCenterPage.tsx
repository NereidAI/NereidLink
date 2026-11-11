// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// CommandCenterPage is the tenant-wide command tracker: filters for state
// (including the compound INFLIGHT value), name and device, and a paged
// table linking each row to its target device detail.

import { useEffect, useMemo, useState } from "react";
import {
  Button,
  Card,
  Input,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useSearchParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import BatchCommandDrawer from "../components/BatchCommandDrawer";
import BatchTasksDrawer from "../components/BatchTasksDrawer";
import type { BatchTask } from "../api/batchCommands";
import DeviceNameCell from "../components/DeviceNameCell";
import { useDebounce } from "../hooks/useDebounce";
import { useServerTable } from "../hooks/useServerTable";
import { listCommands } from "../api/commands";
import { listDevices, type CommandRecord } from "../api/devices";
import { formatDateTime } from "../utils/format";

// terminalFailureStates get a red tag.
const failureStates = new Set(["FAILED", "TIMEOUT", "NOT_CONNECTED", "EXPIRED"]);

// inflightStateSet gets a blue processing tag.
const inflightStateSet = new Set(["PENDING", "CLAIMED", "QUEUED", "DELIVERED"]);

// stateTagColor maps lifecycle states to tag colors.
function stateTagColor(state: string): string {
  if (state === "SUCCEEDED") return "success";
  if (failureStates.has(state)) return "error";
  if (inflightStateSet.has(state)) return "processing";
  return "default";
}

// CommandCenterPage renders the command center.
export default function CommandCenterPage() {
  const { t } = useTranslation();
  const [searchParams, setSearchParams] = useSearchParams();
  const [batchModalOpen, setBatchModalOpen] = useState(false);
  const [batchDrawerOpen, setBatchDrawerOpen] = useState(false);
  const [createdTask, setCreatedTask] = useState<BatchTask | undefined>();

  // stateOptions lists the command center state filter values.
  const stateOptions = [
    { value: "INFLIGHT", label: t("pages.commandCenter.inFlight") },
    { value: "PENDING", label: t("status.pending") },
    { value: "CLAIMED", label: t("status.claimed") },
    { value: "QUEUED", label: t("status.queued") },
    { value: "DELIVERED", label: t("status.delivered") },
    { value: "SUCCEEDED", label: t("status.succeeded") },
    { value: "FAILED", label: t("status.failed") },
    { value: "TIMEOUT", label: t("status.timeout") },
    { value: "NOT_CONNECTED", label: t("status.notConnected") },
    { value: "EXPIRED", label: t("status.expired") },
  ];

  const stateLabels: Record<string, string> = {
    PENDING: t("status.pending"),
    CLAIMED: t("status.claimed"),
    QUEUED: t("status.queued"),
    DELIVERED: t("status.delivered"),
    SUCCEEDED: t("status.succeeded"),
    FAILED: t("status.failed"),
    TIMEOUT: t("status.timeout"),
    NOT_CONNECTED: t("status.notConnected"),
    EXPIRED: t("status.expired"),
  };

  const [stateFilter, setStateFilter] = useState(
    searchParams.get("state") ?? "",
  );
  const [nameInput, setNameInput] = useState(searchParams.get("name") ?? "");
  const [deviceInput, setDeviceInput] = useState(
    searchParams.get("device_id") ?? "",
  );
  const name = useDebounce(nameInput, 300);
  const deviceID = useDebounce(deviceInput, 300);

  const params = useMemo(
    () => ({
      name: name || undefined,
      device_id: deviceID || undefined,
      state: stateFilter || undefined,
    }),
    [name, deviceID, stateFilter],
  );

  const table = useServerTable("command-center", params, listCommands);

  // Map command rows' device ids to their full records for the combined
  // canonical-name/alias cell.
  const devicesQuery = useQuery({
    queryKey: ["devices-all-for-command-center"],
    queryFn: () => listDevices({ page: 1, page_size: 500 }),
  });
  const deviceRecords = new Map(
    (devicesQuery.data?.items ?? []).map((device) => [device.id, device]),
  );

  // Keep the URL in sync so the Overview KPI deep link and browser history
  // stay coherent.
  useEffect(() => {
    const next = new URLSearchParams();
    if (name) next.set("name", name);
    if (deviceID) next.set("device_id", deviceID);
    if (stateFilter) next.set("state", stateFilter);
    setSearchParams(next, { replace: true });
  }, [name, deviceID, stateFilter, setSearchParams]);

  const columns = [
    {
      title: t("common.created"),
      dataIndex: "created_at",
      width: 200,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("common.device"),
      dataIndex: "device_id",
      render: (id: string) => {
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
    { title: t("common.command"), dataIndex: "name" },
    {
      title: t("pages.commandCenter.interaction"),
      dataIndex: "interaction",
      width: 130,
      render: (value: string) => (
        <Typography.Text type="secondary">{value}</Typography.Text>
      ),
    },
    {
      title: t("pages.commandCenter.timeliness"),
      dataIndex: "timeliness",
      width: 110,
      render: (value: string) => (
        <Typography.Text type="secondary">{value}</Typography.Text>
      ),
    },
    {
      title: t("common.state"),
      dataIndex: "state",
      width: 140,
      render: (state: string) => (
        <Tag color={stateTagColor(state)}>
          {stateLabels[state] ?? state}
        </Tag>
      ),
    },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.commandCenter.title")}
        subtitle={t("pages.commandCenter.subtitle")}
        helpId="commands"
        extra={
          <Space>
            <Button type="primary" onClick={() => setBatchModalOpen(true)}>
              {t("pages.commandCenter.batchCommand")}
            </Button>
            <Button onClick={() => setBatchDrawerOpen(true)}>
              {t("pages.commandCenter.batchTasks")}
            </Button>
          </Space>
        }
      />
      <Card>
        <FilterSearchBar
          extra={
            <Input
              allowClear
              style={{ width: 220 }}
              placeholder={t("pages.commandCenter.deviceName")}
              value={deviceInput}
              onChange={(event) => setDeviceInput(event.target.value)}
            />
          }
          filters={[
            {
              key: "state",
              placeholder: t("pages.commandCenter.allStates"),
              value: stateFilter,
              onChange: setStateFilter,
              options: stateOptions,
            },
          ]}
          searchValue={nameInput}
          searchPlaceholder={t("pages.commandCenter.commandName")}
          onSearchChange={setNameInput}
        />
        <Table
          rowKey="id"
          loading={table.loading}
          columns={columns}
          dataSource={table.items as CommandRecord[]}
          pagination={table.pagination}
          onChange={(pagination) =>
            table.onChange({
              current: pagination.current,
              pageSize: pagination.pageSize,
            })
          }
        />
      </Card>
      <BatchCommandDrawer
        open={batchModalOpen}
        onClose={() => setBatchModalOpen(false)}
        onCreated={(task) => {
          setCreatedTask(task);
          setBatchModalOpen(false);
          setBatchDrawerOpen(true);
        }}
      />
      {batchDrawerOpen ? (
        <BatchTasksDrawer
          open
          initialTaskId={createdTask?.id}
          onClose={() => setBatchDrawerOpen(false)}
        />
      ) : null}
    </>
  );
}
