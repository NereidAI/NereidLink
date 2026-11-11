// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceServicesPage is the visible, operator-editable catalog of supported
// EdgeX device services / protocol families (ADR-0033). It lists the global
// rows seeded from the EdgeX 4.0.2 device-service index plus any custom
// family, with keyword search and create/edit/delete actions. Builtin rows
// are editable but cannot be deleted. The same catalog backs the structured
// profile connector and point forms.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import ProtocolFamilyDrawer from "../components/ProtocolFamilyDrawer";
import {
  deleteProtocolFamily,
  listProtocolFamilies,
  type ProtocolFamily,
} from "../api/protocolCatalog";
import { extractError } from "../api/client";
import { protocolCatalogQueryKey } from "../components/connector/useProtocolCatalog";

// DeviceServicesPage renders the device-service catalog management page.
export default function DeviceServicesPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [keyword, setKeyword] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [editing, setEditing] = useState<ProtocolFamily | undefined>();

  const query = useQuery({
    queryKey: protocolCatalogQueryKey,
    queryFn: () => listProtocolFamilies(),
  });
  const filtered = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    if (!needle) return query.data ?? [];
    return (query.data ?? []).filter((f) =>
      [f.family, f.display_name, f.service_name, f.protocol_key]
        .join(" ")
        .toLowerCase()
        .includes(needle),
    );
  }, [query.data, keyword]);

  const deleteMutation = useMutation({
    mutationFn: deleteProtocolFamily,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: protocolCatalogQueryKey });
      message.success(t("pages.deviceServices.toasts.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const openCreate = () => {
    setEditing(undefined);
    setDrawerOpen(true);
  };
  const openEdit = (family: ProtocolFamily) => {
    setEditing(family);
    setDrawerOpen(true);
  };

  const columns: ColumnsType<ProtocolFamily> = [
    {
      title: t("pages.deviceServices.table.displayName"),
      dataIndex: "display_name",
      render: (value: string, row) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{value}</Typography.Text>
          <Typography.Text type="secondary" code style={{ fontSize: 12 }}>
            {row.family}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t("pages.deviceServices.table.serviceName"),
      dataIndex: "service_name",
      width: 200,
      render: (value: string) => value || <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: t("pages.deviceServices.table.protocolKey"),
      dataIndex: "protocol_key",
      width: 150,
      render: (value: string) =>
        value ? <Tag>{value}</Tag> : <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: t("pages.deviceServices.table.transports"),
      dataIndex: "transports",
      render: (_: unknown, row) => (
        <Space size={4} wrap>
          {row.transports.length === 0 && <Typography.Text type="secondary">—</Typography.Text>}
          {row.transports.map((tr) => (
            <Tag key={tr.value}>{tr.label || tr.value}</Tag>
          ))}
        </Space>
      ),
    },
    {
      title: (
        <Tooltip title={t("pages.deviceServices.table.connectionFieldsHint")}>
          {t("pages.deviceServices.table.connectionFields")}
        </Tooltip>
      ),
      key: "connectionFields",
      width: 130,
      render: (_: unknown, row) => (
        <Tag color="blue">{row.connection_fields.length}</Tag>
      ),
    },
    {
      title: (
        <Tooltip title={t("pages.deviceServices.table.pointFieldsHint")}>
          {t("pages.deviceServices.table.pointFields")}
        </Tooltip>
      ),
      key: "pointFields",
      width: 130,
      render: (_: unknown, row) => (
        <Tag color="geekblue">{row.point_fields.length}</Tag>
      ),
    },
    {
      title: t("pages.deviceServices.table.source"),
      dataIndex: "builtin",
      width: 110,
      render: (builtin: boolean) =>
        builtin ? (
          <Tag color="green">{t("pages.deviceServices.source.official")}</Tag>
        ) : (
          <Tag color="gold">{t("pages.deviceServices.source.custom")}</Tag>
        ),
    },
    {
      title: "",
      key: "actions",
      width: 110,
      render: (_: unknown, row) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button type="text" icon={<EditOutlined />} onClick={() => openEdit(row)} />
          </Tooltip>
          {row.builtin ? (
            <Tooltip title={t("pages.deviceServices.builtinNoDelete")}>
              <Button type="text" disabled icon={<DeleteOutlined />} />
            </Tooltip>
          ) : (
            <Popconfirm
              title={t("pages.deviceServices.confirmDelete", { name: row.display_name })}
              okText={t("common.delete")}
              cancelText={t("common.cancel")}
              okButtonProps={{ danger: true }}
              onConfirm={() => deleteMutation.mutate(row.family)}
            >
              <Button type="text" danger icon={<DeleteOutlined />} />
            </Popconfirm>
          )}
        </Space>
      ),
    },
  ];

  return (
    <div>
      <PageHeader
        title={t("nav.deviceServices")}
        helpId="device-services"
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            {t("pages.deviceServices.add")}
          </Button>
        }
      />
      <Card styles={{ body: { paddingTop: 8 } }}>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.deviceServices.searchPlaceholder")}
        />
        <Table
          rowKey="family"
          loading={query.isLoading}
          columns={columns}
          dataSource={filtered}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          size="middle"
        />
      </Card>
      <ProtocolFamilyDrawer
        key={drawerOpen ? (editing?.family ?? "new") : "closed"}
        open={drawerOpen}
        family={editing}
        onClose={() => setDrawerOpen(false)}
      />
    </div>
  );
}
