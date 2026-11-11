// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DevicesPage is the device list: PageHeader, Card, FilterSearchBar and a
// server-paged Table. Rows come from REST only; the online/offline column
// is patched in place from presence pushes. Lifecycle actions cover the
// ADR-0013 claiming, transfer and decommission flows.

import { useEffect, useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Dropdown,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  LoginOutlined,
  MoreOutlined,
  PlusOutlined,
  RedoOutlined,
  SwapOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import dayjs from "dayjs";

import FilterSearchBar from "../components/FilterSearchBar";
import PageHeader from "../components/PageHeader";
import DeviceFormDrawer from "../components/DeviceFormDrawer";
import StatusBadge from "../components/StatusBadge";
import ClaimDeviceDrawer from "../components/ClaimDeviceDrawer";
import ClaimTokenDrawer from "../components/ClaimTokenDrawer";
import TransferDeviceDrawer from "../components/TransferDeviceDrawer";
import BulkImportDrawer from "../components/BulkImportDrawer";
import { useDebounce } from "../hooks/useDebounce";
import { usePresenceSince } from "../hooks/usePresenceSince";
import { useServerTable } from "../hooks/useServerTable";
import {
  listDevices,
  listProfiles,
  type Device,
  bulkImportDevices,
  bulkImportTemplateUrl,
  previewDeviceImport,
  bulkDecommissionDevices,
  type BulkDecommissionResult,
} from "../api/devices";
import {
  decommissionDevice,
  issueClaimToken,
  listCustomers,
  listDeviceGroups,
  reactivateDevice,
  type ClaimToken,
  releaseFromQuarantine,
} from "../api/ownership";
import { useWSStore } from "../store/wsStore";
import { ConnectorStateColor as connectorStateColor } from "../utils/connectorState";

// DevicesPage renders the device list page.
export default function DevicesPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const [searchInput, setSearchInput] = useState("");
  const search = useDebounce(searchInput, 300);
  // Deep links from Customers/Groups arrive with ?customer_id / ?group_id.
  const initialQuery = new URLSearchParams(window.location.search);
  const [profileFilter, setProfileFilter] = useState(initialQuery.get("profile_id") ?? "");
  // The list opens on ACTIVE devices; the explicit "all states" option
  // (value "ALL", also what an antd allow-clear yields) asks the backend to
  // include DECOMMISSIONED rows rather than applying its empty-value default
  // visible set.
  const [stateFilter, setStateFilter] = useState("ACTIVE");
  const [customerFilter, setCustomerFilter] = useState(initialQuery.get("customer_id") ?? "");
  const [groupFilter, setGroupFilter] = useState(initialQuery.get("group_id") ?? "");
  const [typeFilter, setTypeFilter] = useState("");
  const [connectionFilter, setConnectionFilter] = useState("");
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<Device | undefined>();

  const [claimModal, setClaimModal] = useState(false);
  const [claimTarget, setClaimTarget] = useState<Device | undefined>();
  const [tokenModal, setTokenModal] = useState(false);
  const [issuedToken, setIssuedToken] = useState<ClaimToken | undefined>();
  const [transferModal, setTransferModal] = useState(false);
  const [transferTarget, setTransferTarget] = useState<Device | undefined>();
  const [importModal, setImportModal] = useState(false);
  const [selectedRowKeys, setSelectedRowKeys] = useState<string[]>([]);

  const profilesQuery = useQuery({ queryKey: ["profiles"], queryFn: listProfiles });
  // Memoized so derived memos keep stable dependency identities instead
  // of seeing fresh [] fallbacks on every render.
  const profiles = useMemo(() => profilesQuery.data ?? [], [profilesQuery.data]);
  const customersQuery = useQuery({ queryKey: ["customers"], queryFn: listCustomers });
  const customers = useMemo(() => customersQuery.data ?? [], [customersQuery.data]);
  const groupsQuery = useQuery({ queryKey: ["device-groups"], queryFn: listDeviceGroups });
  const groups = groupsQuery.data ?? [];

  const params = useMemo(
    () => ({
      search: search || undefined,
      profile_id: profileFilter || undefined,
      customer_id: customerFilter || undefined,
      group_id: groupFilter || undefined,
      state: stateFilter || "ALL",
      type: typeFilter || undefined,
      connection: connectionFilter || undefined,
    }),
    [search, profileFilter, customerFilter, groupFilter, stateFilter, typeFilter, connectionFilter],
  );

  const table = useServerTable("devices", params, listDevices);

  const presenceSince = usePresenceSince();
  const subscribe = useWSStore((state) => state.subscribe);
  const unsubscribe = useWSStore((state) => state.unsubscribe);
  const connectorEvents = useWSStore((state) => state.connectorEvents);

  // Subscribe presence for the visible rows. Presence frames only carry
  // state changes, so the list starts UNKNOWN until the next boundary.
  useEffect(() => {
    subscribe("devices-presence", "presence", table.items.map((d) => d.id));
  }, [subscribe, table.items]);

  // Connector lifecycle events tenant-wide; any frame may alter a row's
  // connector_state/type, so re-fetch the current devices page. The
  // subscription is tenant-scoped like the gateway Connectors tab.
  useEffect(() => {
    subscribe("devices-connectors", "connector", []);
    return () => unsubscribe("devices-connectors");
  }, [subscribe, unsubscribe]);

  useEffect(() => {
    if (connectorEvents.length === 0) return;
    void queryClient.invalidateQueries({ queryKey: ["devices"] });
    // Only a newly arrived frame should trigger a refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectorEvents.length]);

  const profileName = useMemo(() => {
    const map = new Map(profiles.map((profile) => [profile.id, profile.name]));
   	return (id?: string) => (id ? (map.get(id) ?? id) : "");
  }, [profiles]);

  const customerName = useMemo(() => {
    const map = new Map(customers.map((customer) => [customer.id, customer.name]));
    return (id?: string) => (id ? map.get(id) ?? id : "");
  }, [customers]);

  const tokenMutation = useMutation({
    mutationFn: (device: Device) => issueClaimToken(device.id),
    onSuccess: (token) => {
      setIssuedToken(token);
      setTokenModal(true);
    },
    onError: () => message.error(t("pages.devices.toasts.tokenFailed")),
  });

  const decommissionMutation = useMutation({
    mutationFn: (device: Device) => decommissionDevice(device.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["devices"] });
      message.success(t("pages.devices.toasts.decommissioned"));
    },
    onError: () => message.error(t("pages.devices.toasts.decommissionFailed")),
  });

  const reactivateMutation = useMutation({
    mutationFn: (device: Device) => reactivateDevice(device.id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["devices"] });
      message.success(t("pages.devices.toasts.reactivated"));
    },
    onError: () => message.error(t("pages.devices.toasts.reactivateFailed")),
  });

  const bulkDecommissionMutation = useMutation({
    mutationFn: (ids: string[]) => bulkDecommissionDevices(ids),
    onSuccess: (result: BulkDecommissionResult) => {
      setSelectedRowKeys([]);
      queryClient.invalidateQueries({ queryKey: ["devices"] });
      if (result.skipped.length === 0) {
        message.success(t("pages.devices.toasts.bulkDecommissioned", { count: result.decommissioned }));
      } else {
        message.warning(t("pages.devices.toasts.bulkDecommissionPartial", {
          decommissioned: result.decommissioned,
          skipped: result.skipped.length,
        }));
      }
    },
    onError: () => message.error(t("pages.devices.toasts.bulkDecommissionFailed")),
  });

  const lifecycleMenu = (device: Device): MenuProps["items"] => {
    if (device.state === "DECOMMISSIONED") {
      return [
        {
          key: "reactivate",
          icon: <RedoOutlined />,
          label: t("pages.devices.lifecycle.reactivate"),
          onClick: () => reactivateMutation.mutate(device),
        },
      ];
    }

    if (device.state === "QUARANTINED") {
      return [
        {
          key: "release",
          icon: <RedoOutlined />,
          label: t("pages.devices.lifecycle.release"),
          onClick: () => {
            const note = window.prompt(t("pages.devices.lifecycle.releasePrompt"));
            if (note) {
              releaseFromQuarantine(device.id, note)
                .then(async () => {
                  await queryClient.invalidateQueries({ queryKey: ["devices"] });
                  message.success(t("pages.devices.toasts.released"));
                })
                .catch(() => message.error(t("pages.devices.toasts.releaseFailed")));
            }
          },
        },
      ];
    }

    const items: NonNullable<MenuProps["items"]> = [];
    if (device.state === "UNCLAIMED") {
      items.push(
        {
          key: "token",
          icon: <KeyOutlined />,
          label: t("pages.devices.lifecycle.issueToken"),
          onClick: () => tokenMutation.mutate(device),
        },
        {
          key: "claim",
          icon: <LoginOutlined />,
          label: t("pages.devices.lifecycle.claim"),
          onClick: () => {
            setClaimTarget(device);
            setClaimModal(true);
          },
        },
      );
    }
    if (device.state === "ACTIVE") {
      items.push({
        key: "transfer",
        icon: <SwapOutlined />,
        label: t("pages.devices.lifecycle.transfer"),
        onClick: () => {
          setTransferTarget(device);
          setTransferModal(true);
        },
      });
    }
    items.push({
      key: "decommission",
      danger: true,
      icon: <DeleteOutlined />,
      label: t("pages.devices.lifecycle.decommission"),
      onClick: () => decommissionMutation.mutate(device),
    });
    return items;
  };

  const columns = [
    {
      title: t("common.name"),
      dataIndex: "canonical_name",
      render: (name: string, device: Device) => (
        <Space direction="vertical" size={0}>
          <Link to={`/devices/${device.id}`}>{name}</Link>
          {device.alias && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {device.alias}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.devices.typeColumn"),
      dataIndex: "is_gateway",
      key: "type",
      width: 120,
      render: (isGateway: boolean, device: Device) => {
        if (isGateway) {
          return <Tag color="gold">{t("pages.devices.type.gateway")}</Tag>;
        }
        if (device.gateway_id) {
          return <Tag color="cyan">{t("pages.devices.type.subdevice")}</Tag>;
        }
        return <Tag color="blue">{t("pages.devices.type.device")}</Tag>;
      },
    },
    {
      title: t("pages.devices.connectorColumn"),
      key: "connector",
      width: 180,
      render: (_: unknown, device: Device) => {
        // A gateway row summarizes its gateway-level instances
        // (device_id IS NULL); bound instances appear on their own
        // sub-device rows.
        if (device.is_gateway) {
          const count = device.gateway_connector_count ?? 0;
          if (count === 0) {
            return <Typography.Text type="secondary">-</Typography.Text>;
          }
          return (
            <Tooltip title={t("pages.devices.connectorGatewayLevel", { count })}>
              <Link to={`/devices/${device.id}?tab=connectors`}>
                <Tag color="geekblue">{count}</Tag>
              </Link>
            </Tooltip>
          );
        }
        if (!device.connector_type) {
          return <Typography.Text type="secondary">-</Typography.Text>;
        }
        // A bound sub-device links straight to its gateway's Connectors
        // tab so the operator can inspect the spec without searching the
        // gateway first. The tag's tooltip renders the localized apply
        // state (pages.connectors.states.* vocabulary).
        const stateLabel = device.connector_state
          ? t(`pages.connectors.states.${device.connector_state}`, {
              defaultValue: device.connector_state,
            })
          : undefined;
        const tag = (
          <Tooltip title={stateLabel}>
            <Tag color={connectorStateColor[device.connector_state ?? ""] ?? "default"}>
              {device.connector_type}
            </Tag>
          </Tooltip>
        );
        return device.gateway_id ? (
          <Link to={`/devices/${device.gateway_id}?tab=connectors`}>{tag}</Link>
        ) : (
          tag
        );
      },
    },
    {
      title: t("pages.devices.profileColumn"),
      dataIndex: "profile_id",
      render: (id?: string) => profileName(id) || <Typography.Text type="secondary">-</Typography.Text>,
    },
    {
      title: t("pages.devices.customerColumn"),
      dataIndex: "customer_id",
      render: (id?: string) => customerName(id) || <Typography.Text type="secondary">-</Typography.Text>,
    },
    {
      title: (
        <ColumnTip
          label={t("pages.devices.lifecycleColumn")}
          tip={t("pages.devices.lifecycleTip")}
        />
      ),
      dataIndex: "state",
      width: 150,
      render: (state: string) => (
        <Typography.Text
          type={state === "ACTIVE" ? "success"
            : state === "UNCLAIMED" ? "warning" : "danger"}
        >
          {t(`pages.devices.states.${state}`)}
        </Typography.Text>
      ),
    },
    {
      title: (
        <ColumnTip
          label={t("pages.devices.connectionColumn")}
          tip={t("pages.devices.connectionTip")}
        />
      ),
      key: "status",
      width: 130,
      render: (_: unknown, device: Device) => {
        const boundary = presenceSince.get(device.id);
        const state = boundary?.state ?? device.presence_state ?? "UNKNOWN";
        const since = boundary?.time ?? device.presence_since;
        const fmt = i18n.language.startsWith("zh")
          ? "YYYY/MM/DD HH:mm:ss"
          : "M/D/YYYY, h:mm:ss A";
        // The badge already shows the state, so the hover shows a time, not
        // the same word: last activity (latest telemetry/presence), falling
        // back to the current connection boundary's start.
        const tooltipAt = device.last_activity_at ?? since;
        if (!tooltipAt) {
          return <StatusBadge status={state} />;
        }
        const label = device.last_activity_at
          ? t("pages.devices.lastActivity")
          : t("pages.devices.connectionTooltip");
        return (
          <Tooltip title={`${label} ${dayjs(tooltipAt).format(fmt)}`}>
            <StatusBadge status={state} />
          </Tooltip>
        );
      },
    },
    {
      title: t("pages.devices.sinceColumn"),
      key: "created_at",
      width: 210,
      render: (_: unknown, device: Device) => {
        if (!device.created_at) {
          return <Typography.Text type="secondary">-</Typography.Text>;
        }
        const fmt = i18n.language.startsWith("zh")
          ? "YYYY/MM/DD HH:mm:ss"
          : "M/D/YYYY, h:mm:ss A";
        return (
          <Typography.Text type="secondary">
            {dayjs(device.created_at).format(fmt)}
          </Typography.Text>
        );
      },
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 120,
      render: (_: unknown, device: Device) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setEditing(device);
                setModalOpen(true);
              }}
            />
          </Tooltip>
          <Dropdown menu={{ items: lifecycleMenu(device) }} trigger={["click"]}>
            <Button type="text" size="small" icon={<MoreOutlined />} />
          </Dropdown>
        </Space>
      ),
    },
  ];

  const stateOptions = [
    { value: "ACTIVE", label: t("pages.devices.states.ACTIVE") },
    { value: "UNCLAIMED", label: t("pages.devices.states.UNCLAIMED") },
    { value: "QUARANTINED", label: t("pages.devices.states.QUARANTINED") },
    { value: "DECOMMISSIONED", label: t("pages.devices.states.DECOMMISSIONED") },
  ];

  const connectionOptions = [
    { value: "online", label: t("pages.devices.connection.online") },
    { value: "offline", label: t("pages.devices.connection.offline") },
    { value: "unknown", label: t("pages.devices.connection.unknown") },
  ];

  return (
    <>
      <PageHeader
        title={t("pages.devices.title")}
        subtitle={t("pages.devices.subtitle")}
        helpId="devices"
        extra={
          <Space>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => {
                setEditing(undefined);
                setModalOpen(true);
              }}
            >
              {t("pages.devices.create")}
            </Button>
            <Button
              icon={<UploadOutlined />}
              onClick={() => setImportModal(true)}
            >
              {t("pages.devices.bulk.import")}
            </Button>
            <Button
              type="primary"
              danger
              disabled={selectedRowKeys.length === 0}
              icon={<DeleteOutlined />}
              onClick={() => {
                if (window.confirm(t("pages.devices.bulk.confirmDecommission"))) {
                  bulkDecommissionMutation.mutate(selectedRowKeys);
                }
              }}
            >
              {t("pages.devices.bulk.decommission")}
            </Button>
          </Space>
        }
      />
      <Card>
        <FilterSearchBar
          filters={[
            {
              key: "type",
              placeholder: t("pages.devices.typeColumn"),
              value: typeFilter,
              onChange: (value) => setTypeFilter(value),
              options: [
                { value: "gateway", label: t("pages.devices.type.gateway") },
                { value: "subdevice", label: t("pages.devices.type.subdevice") },
                { value: "device", label: t("pages.devices.type.device") },
              ],
            },
            {
              key: "profile_id",
              placeholder: t("pages.devices.profileColumn"),
              value: profileFilter,
              onChange: (value) => setProfileFilter(value),
              options: profiles.map((profile) => ({
                value: profile.id,
                label: profile.name,
              })),
            },
            {
              key: "customer_id",
              placeholder: t("pages.devices.customerColumn"),
              value: customerFilter,
              onChange: (value) => setCustomerFilter(value),
              options: customers.map((customer) => ({
                value: customer.id,
                label: customer.name,
              })),
            },
            {
              key: "group_id",
              placeholder: t("pages.devices.groupColumn"),
              value: groupFilter,
              onChange: (value) => setGroupFilter(value),
              options: groups.map((group) => ({
                value: group.id,
                label: group.name,
              })),
            },
            {
              key: "state",
              placeholder: t("pages.devices.lifecycleColumn"),
              value: stateFilter,
              onChange: (value: string) => setStateFilter(value),
              options: stateOptions,
            },
            {
              key: "connection",
              placeholder: t("pages.devices.connectionColumn"),
              value: connectionFilter,
              onChange: (value: string) => setConnectionFilter(value),
              options: connectionOptions,
            },
          ]}
          searchValue={searchInput}
          searchPlaceholder={t("pages.devices.searchPlaceholder")}
          onSearchChange={setSearchInput}
        />
        <Table
          rowKey="id"
          rowSelection={{
            selectedRowKeys,
            onChange: (keys) => setSelectedRowKeys(keys as string[]),
          }}
          loading={table.loading}
          columns={columns}
          dataSource={table.items}
          pagination={table.pagination}
          onChange={(pagination) =>
            table.onChange({
              current: pagination.current,
              pageSize: pagination.pageSize,
            })
          }
        />
      </Card>
      <DeviceFormDrawer
        open={modalOpen}
        device={editing}
        profiles={profiles}
        onClose={() => setModalOpen(false)}
      />
      <ClaimDeviceDrawer
        open={claimModal}
        device={claimTarget}
        onClose={() => setClaimModal(false)}
      />
      <ClaimTokenDrawer
        open={tokenModal}
        token={issuedToken}
        onClose={() => setTokenModal(false)}
      />
      <TransferDeviceDrawer
        open={transferModal}
        device={transferTarget}
        onClose={() => setTransferModal(false)}
      />
      <BulkImportDrawer
        open={importModal}
        title={t("pages.devices.bulk.importTitle")}
        templateUrl={bulkImportTemplateUrl("xlsx")}
        accept=".csv,.xlsx"
        formatHint="CSV / XLSX"
        preview={previewDeviceImport}
        doImport={bulkImportDevices}
        onImported={() => queryClient.invalidateQueries({ queryKey: ["devices"] })}
        onClose={() => setImportModal(false)}
      />
    </>
  );
}

// ColumnTip renders a table header with an explanatory hover tooltip,
// used for the lifecycle/connection columns users otherwise confuse.
function ColumnTip({ label, tip }: { label: string; tip: string }) {
  return (
    <Tooltip title={tip}>
      <span>{label}</span>
    </Tooltip>
  );
}
