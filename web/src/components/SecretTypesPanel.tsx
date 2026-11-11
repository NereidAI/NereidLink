// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretTypesPanel renders the edge secret type descriptor catalog table
// inside the combined secrets page (ADR-0034). Edge-advertised descriptors
// are read-only with a clone/view action; tenant-defined descriptors are
// full CRUD. Keyword search filters by type key and bilingual display
// names.

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
import {
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";

import FilterSearchBar from "./FilterSearchBar";
import SecretTypeDrawer from "./SecretTypeDrawer";
import {
  deleteSecretType,
  listSecretTypes,
  type SecretType,
} from "../api/secretTypes";
import { extractError } from "../api/client";

// SecretTypesPanel renders the descriptor list and its drawer.
export default function SecretTypesPanel() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [keyword, setKeyword] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [drawerMode, setDrawerMode] =
    useState<"create" | "view" | "edit">("create");
  const [editing, setEditing] = useState<SecretType | undefined>();

  const query = useQuery({
    queryKey: ["edge-secret-types"],
    queryFn: listSecretTypes,
  });
  const filtered = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    if (!needle) return query.data ?? [];
    return (query.data ?? []).filter((row) =>
      [
        row.secret_type,
        row.type_version,
        row.display_name?.en,
        row.display_name?.zh,
      ]
        .join(" ")
        .toLowerCase()
        .includes(needle),
    );
  }, [query.data, keyword]);

  const deleteMutation = useMutation({
    mutationFn: (row: SecretType) =>
      deleteSecretType(row.secret_type, row.type_version),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["edge-secret-types"] });
      message.success(t("pages.secretTypes.toasts.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const openCreate = () => {
    setEditing(undefined);
    setDrawerMode("create");
    setDrawerOpen(true);
  };
  const openView = (row: SecretType) => {
    setEditing(row);
    setDrawerMode("view");
    setDrawerOpen(true);
  };
  const openEdit = (row: SecretType) => {
    setEditing(row);
    setDrawerMode("edit");
    setDrawerOpen(true);
  };

  const columns: ColumnsType<SecretType> = [
    {
      title: t("pages.secretTypes.table.type"),
      render: (_: unknown, row) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{row.secret_type}</Typography.Text>
          <Typography.Text type="secondary" style={{ fontSize: 12 }}>
            {row.display_name?.en}
            {row.display_name?.zh ? ` / ${row.display_name.zh}` : ""}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t("pages.secretTypes.table.version"),
      dataIndex: "type_version",
      width: 120,
      render: (value: string) =>
        value ? <Tag>{value}</Tag> : <Typography.Text type="secondary">—</Typography.Text>,
    },
    {
      title: t("pages.secretTypes.table.fields"),
      key: "fieldCount",
      width: 100,
      render: (_: unknown, row) => {
        const properties = (row.fields_schema.properties ?? {}) as Record<string, unknown>;
        return <Tag color="blue">{Object.keys(properties).length}</Tag>;
      },
    },
    {
      title: t("pages.secretTypes.table.source"),
      dataIndex: "source",
      width: 110,
      render: (source: SecretType["source"]) => {
        if (source === "edge")
          return <Tag color="green">{t("pages.secretTypes.sources.edge")}</Tag>;
        return <Tag color="gold">{t("pages.secretTypes.sources.manual")}</Tag>;
      },
    },
    {
      title: "",
      key: "actions",
      width: 130,
      render: (_: unknown, row) => (
        <Space>
          <Tooltip title={t("common.view")}>
            <Button type="text" icon={<EyeOutlined />} onClick={() => openView(row)} />
          </Tooltip>
          {row.source === "manual" ? (
            <>
              <Tooltip title={t("common.edit")}>
                <Button type="text" icon={<EditOutlined />} onClick={() => openEdit(row)} />
              </Tooltip>
              <Popconfirm
                title={t("pages.secretTypes.confirmDelete", { name: row.secret_type })}
                okText={t("common.delete")}
                cancelText={t("common.cancel")}
                okButtonProps={{ danger: true }}
                onConfirm={() => deleteMutation.mutate(row)}
              >
                <Button type="text" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </>
          ) : (
            <Tooltip title={t("pages.secretTypes.clone")}>
              <Button type="text" icon={<CopyOutlined />} onClick={() => openView(row)} />
            </Tooltip>
          )}
        </Space>
      ),
    },
  ];

  return (
    <Card styles={{ body: { paddingTop: 8 } }}>
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          {t("pages.secretTypes.add")}
        </Button>
      </Space>
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t("pages.secretTypes.searchPlaceholder")}
      />
      <Table
        rowKey={(row) => `${row.secret_type}|${row.type_version}`}
        loading={query.isLoading}
        columns={columns}
        dataSource={filtered}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        size="middle"
      />
      <SecretTypeDrawer
        key={
          drawerOpen
            ? `${editing ? `${editing.secret_type}|${editing.type_version}` : "new"}#${drawerMode}`
            : "closed"
        }
        open={drawerOpen}
        mode={drawerMode}
        descriptor={editing}
        onClose={() => setDrawerOpen(false)}
        onSaved={() => {
          setDrawerOpen(false);
          void queryClient.invalidateQueries({ queryKey: ["edge-secret-types"] });
        }}
        onSwitchEdit={() => setDrawerMode("edit")}
      />
    </Card>
  );
}
