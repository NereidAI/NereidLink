// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ForwardersPage manages third-party data forwarding (ADR-0018). The
// Forwarders tab lists the tenant forwarders and opens the create/edit
// drawer; the Dead letters tab inspects parked envelopes and re-publishes
// them manually. All delivery is asynchronous, so this page is REST-only.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
} from "antd";
import {
  DeleteOutlined,
  EditOutlined,
  PlayCircleOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import ForwarderFormDrawer from "../components/ForwarderFormDrawer";
import { forwarderTarget } from "../components/forwarderFieldConfig";
import { formatDateTime } from "../utils/format";
import {
  deleteForwarder,
  listForwarderDeliveries,
  listForwarderDLQ,
  listForwarders,
  replayDeadLetter,
  type DeadLetter,
  type Delivery,
  type Forwarder,
} from "../api/forwarders";

// ForwardersPage renders the forwarder management page.
export default function ForwardersPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState("forwarders");

  return (
    <>
      <PageHeader
        title={t("pages.forwarders.title")}
        subtitle={t("pages.forwarders.subtitle")}
        helpId="forwarders"
      />
      <Card>
        <Tabs
          activeKey={tab}
          onChange={setTab}
          items={[
            {
              key: "forwarders",
              label: t("pages.forwarders.tabs.forwarders"),
              children: <ForwarderList />,
            },
            {
              key: "dlq",
              label: t("pages.forwarders.tabs.dlq"),
              children: <DeadLetterList />,
            },
            {
              key: "deliveries",
              label: t("pages.forwarders.tabs.deliveries"),
              children: <DeliveryLog />,
            },
          ]}
        />
      </Card>
    </>
  );
}

// ForwarderList renders the forwarder table and editor drawer.
function ForwarderList() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<Forwarder | undefined>();
  const [keyword, setKeyword] = useState("");
  const [enabledFilter, setEnabledFilter] = useState("");

  const query = useQuery({
    queryKey: ["forwarders"],
    queryFn: listForwarders,
  });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const forwarders = useMemo(() => query.data ?? [], [query.data]);

  // filtered applies keyword search over the name plus an enabled filter.
  const filtered = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return forwarders.filter((forwarder) => {
      if (enabledFilter && (enabledFilter === "enabled") !== forwarder.enabled) {
        return false;
      }
      if (!needle) return true;
      return forwarder.name.toLowerCase().includes(needle);
    });
  }, [forwarders, keyword, enabledFilter]);

  const deleteMutation = useMutation({
    mutationFn: deleteForwarder,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["forwarders"] });
      message.success(t("pages.forwarders.toasts.deleted"));
    },
    onError: (error) => message.error(String(error)),
  });

  const columns = [
    { title: t("common.name"), dataIndex: "name" },
    {
      title: t("pages.forwarders.columns.sink"),
      dataIndex: "sink_type",
      width: 130,
      render: (type: string) => (
        <Tag color="geekblue">{t(`pages.forwarders.sink.${type}`)}</Tag>
      ),
    },
    {
      title: t("pages.forwarders.columns.target"),
      dataIndex: "sink_config",
      ellipsis: true,
      render: (cfg: Record<string, unknown>, forwarder: Forwarder) => {
        const { method, target } = forwarderTarget(
          forwarder.sink_type,
          cfg,
        );
        return (
          <Tooltip title={target}>
            <span>
              {method ? <Tag color="blue">{method}</Tag> : null}
              {target}
            </span>
          </Tooltip>
        );
      },
    },
    {
      title: t("pages.forwarders.columns.attempts"),
      dataIndex: "max_attempts",
      width: 100,
    },
    {
      title: t("common.enabled"),
      dataIndex: "enabled",
      width: 90,
      render: (enabled: boolean) => (
        <Switch checked={enabled} size="small" disabled />
      ),
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 100,
      render: (_: unknown, forwarder: Forwarder) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setEditing(forwarder);
                setDrawerOpen(true);
              }}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.forwarders.confirm.title")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMutation.mutate(forwarder.id)}
          >
            <Tooltip title={t("common.delete")}>
              <Button type="text" size="small" danger icon={<DeleteOutlined />} />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <>
      <div style={{ position: "relative" }}>
        <Button
          style={{ position: "absolute", left: 0, top: 4, zIndex: 1 }}
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => {
            setEditing(undefined);
            setDrawerOpen(true);
          }}
        >
          {t("pages.forwarders.create")}
        </Button>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.forwarders.filters.searchPlaceholder")}
          filters={[
            {
              key: "enabled",
              placeholder: t("pages.forwarders.filters.enabledAll"),
              value: enabledFilter,
              onChange: setEnabledFilter,
              options: [
                { value: "enabled", label: t("common.filterEnabled") },
                { value: "disabled", label: t("common.filterDisabled") },
              ],
            },
          ]}
        />
      </div>
      <Table
        rowKey="id"
        loading={query.isFetching}
        columns={columns}
        dataSource={filtered}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      <ForwarderFormDrawer
        open={drawerOpen}
        forwarder={editing}
        onClose={() => setDrawerOpen(false)}
      />
    </>
  );
}

// DeadLetterList renders the forwarder DLQ with per-row replay.
function DeadLetterList() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [forwarderFilter, setForwarderFilter] = useState("");

  const forwardersQuery = useQuery({
    queryKey: ["forwarders"],
    queryFn: listForwarders,
  });
  const forwarderOptions = (forwardersQuery.data ?? []).map((forwarder) => ({
    value: forwarder.id,
    label: forwarder.name,
  }));

  const query = useQuery({
    queryKey: ["forwarder-dlq", forwarderFilter],
    queryFn: () => listForwarderDLQ(forwarderFilter),
  });
  const rows = query.data ?? [];

  const replayMutation = useMutation({
    mutationFn: replayDeadLetter,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["forwarder-dlq"] });
      message.success(t("pages.forwarders.toasts.replayed"));
    },
    onError: (error) => message.error(String(error)),
  });

  const forwarderName = useMemo(() => {
    const map = new Map<string, string>();
    (forwardersQuery.data ?? []).forEach((forwarder) =>
      map.set(forwarder.id, forwarder.name),
    );
    return map;
  }, [forwardersQuery.data]);

  const columns = [
    {
      title: t("pages.forwarders.dlq.createdAt"),
      dataIndex: "created_at",
      width: 190,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.forwarders.dlq.forwarder"),
      dataIndex: "forwarder_id",
      width: 160,
      render: (id: string) => (
        <Tag color="orange">{forwarderName.get(id) ?? id}</Tag>
      ),
    },
    { title: t("common.device"), dataIndex: ["payload", "device_id"], width: 200 },
    { title: t("pages.forwarders.dlq.attempts"), dataIndex: "attempts", width: 90 },
    { title: t("pages.forwarders.dlq.error"), dataIndex: "error", ellipsis: true },
    {
      title: t("common.actions"),
      key: "actions",
      width: 90,
      render: (_: unknown, row: DeadLetter) => (
        <Popconfirm
          title={t("pages.forwarders.confirm.replayTitle")}
          okText={t("pages.forwarders.dlq.replay")}
          onConfirm={() => replayMutation.mutate(row.id)}
        >
          <Tooltip title={t("pages.forwarders.dlq.replay")}>
            <Button
              type="text"
              size="small"
              icon={<PlayCircleOutlined />}
              loading={replayMutation.isPending}
            />
          </Tooltip>
        </Popconfirm>
      ),
    },
  ];

  return (
    <>
      <FilterSearchBar
        filters={[
          {
            key: "forwarder",
            placeholder: t("pages.forwarders.filters.forwarderAll"),
            value: forwarderFilter,
            onChange: setForwarderFilter,
            options: forwarderOptions,
          },
        ]}
      />
      <Table
        rowKey="id"
        loading={query.isFetching}
        columns={columns}
        dataSource={rows}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
    </>
  );
}

// statusColor maps a delivery status to its tag color.
function statusColor(status: Delivery["status"]): string {
  switch (status) {
    case "delivered":
      return "success";
    case "retry":
      return "warning";
    default:
      return "error";
  }
}

// DeliveryLog renders the per-forwarder delivery history with receiver
// return codes. It is read-only.
function DeliveryLog() {
  const { t } = useTranslation();
  const [forwarderFilter, setForwarderFilter] = useState("");

  const forwardersQuery = useQuery({
    queryKey: ["forwarders"],
    queryFn: listForwarders,
  });
  const forwarderOptions = (forwardersQuery.data ?? []).map((forwarder) => ({
    value: forwarder.id,
    label: forwarder.name,
  }));

  const query = useQuery({
    queryKey: ["forwarder-deliveries", forwarderFilter],
    queryFn: () => listForwarderDeliveries(forwarderFilter),
  });
  const rows = query.data ?? [];

  const forwarderName = useMemo(() => {
    const map = new Map<string, string>();
    (forwardersQuery.data ?? []).forEach((forwarder) =>
      map.set(forwarder.id, forwarder.name),
    );
    return map;
  }, [forwardersQuery.data]);

  const columns = [
    {
      title: t("pages.forwarders.deliveries.occurredAt"),
      dataIndex: "occurred_at",
      width: 190,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.forwarders.deliveries.forwarder"),
      dataIndex: "forwarder_id",
      width: 150,
      render: (id: string) => (
        <Tag color="geekblue">{forwarderName.get(id) ?? id}</Tag>
      ),
    },
    { title: t("common.device"), dataIndex: "device_id", width: 180, ellipsis: true },
    {
      title: t("pages.forwarders.deliveries.status"),
      dataIndex: "status",
      width: 110,
      render: (status: Delivery["status"]) => (
        <Tag color={statusColor(status)}>
          {t(`pages.forwarders.deliveries.status_${status}`)}
        </Tag>
      ),
    },
    { title: t("pages.forwarders.deliveries.code"), dataIndex: "code", width: 90 },
    { title: t("pages.forwarders.deliveries.attempts"), dataIndex: "attempts", width: 90 },
    {
      title: t("pages.forwarders.deliveries.reason"),
      dataIndex: "reason",
      ellipsis: true,
      render: (reason: string) =>
        reason ? <Tooltip title={reason}>{reason}</Tooltip> : "",
    },
  ];

  return (
    <>
      <FilterSearchBar
        filters={[
          {
            key: "forwarder",
            placeholder: t("pages.forwarders.filters.forwarderAll"),
            value: forwarderFilter,
            onChange: setForwarderFilter,
            options: forwarderOptions,
          },
        ]}
      />
      <Table
        rowKey="id"
        loading={query.isFetching}
        columns={columns}
        dataSource={rows}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
    </>
  );
}