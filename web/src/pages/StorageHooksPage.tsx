// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// StorageHooksPage is the pre-storage hook list: header, card with table
// and the shared create/edit modal. REST-only; hooks change only through
// user mutations.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from "antd";
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import StorageHookFormDrawer from "../components/StorageHookFormDrawer";
import {
  deleteStorageHook,
  listStorageHooks,
  type StorageHook,
} from "../api/hooks";

// StorageHooksPage renders the hook management page.
export default function StorageHooksPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<StorageHook | undefined>();
  const [keyword, setKeyword] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [enabledFilter, setEnabledFilter] = useState("");

  const query = useQuery({
    queryKey: ["storage-hooks"],
    queryFn: listStorageHooks,
  });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const hooks = useMemo(() => query.data ?? [], [query.data]);

  // filteredHooks applies client-side keyword search over the hook name
  // plus type and enabled/disabled enum filters; empty values mean all.
  const filteredHooks = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return hooks.filter((hook) => {
      if (typeFilter && hook.hook_type !== typeFilter) {
        return false;
      }
      if (enabledFilter && (enabledFilter === "enabled") !== hook.enabled) {
        return false;
      }
      if (!needle) return true;
      return hook.name.toLowerCase().includes(needle);
    });
  }, [hooks, keyword, typeFilter, enabledFilter]);

  const deleteMutation = useMutation({
    mutationFn: deleteStorageHook,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["storage-hooks"] });
      message.success(t("pages.storageHooks.toasts.deleted"));
    },
    onError: () => message.error(t("pages.storageHooks.toasts.deleteFailed")),
  });

  const hookTypeLabel: Record<string, string> = {
    expression: t("enums.expression"),
    webhook: t("enums.webhook"),
    wasm: t("enums.wasm"),
  };

  const policyLabel: Record<string, string> = {
    passthrough: t("enums.passthrough"),
    abort: t("enums.abort"),
    dlq: t("enums.dlq"),
  };

  const columns = [
    { title: t("pages.storageHooks.order"), dataIndex: "order", width: 70 },
    { title: t("common.name"), dataIndex: "name" },
    {
      title: t("common.type"),
      dataIndex: "hook_type",
      width: 110,
      render: (type: string) => <Tag>{hookTypeLabel[type] ?? type}</Tag>,
    },
    {
      title: t("pages.storageHooks.keys"),
      dataIndex: "measurement_keys",
      ellipsis: true,
      render: (keys: string[]) =>
        keys.length === 0 ? (
          <Tag color="blue">{t("common.all")}</Tag>
        ) : (
          keys.join(", ")
        ),
    },
    {
      title: t("pages.storageHooks.policy"),
      dataIndex: "failure_policy",
      width: 110,
      render: (policy: string) => (
        <Tag color="purple">{policyLabel[policy] ?? policy}</Tag>
      ),
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
      render: (_: unknown, hook: StorageHook) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setEditing(hook);
                setModalOpen(true);
              }}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.storageHooks.confirm.title")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMutation.mutate(hook.id)}
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
      <PageHeader
        title={t("pages.storageHooks.title")}
        subtitle={t("pages.storageHooks.subtitle")}
        helpId="storage-hooks"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(undefined);
              setModalOpen(true);
            }}
          >
            {t("pages.storageHooks.create")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.storageHooks.filters.searchPlaceholder")}
          filters={[
            {
              key: "hook_type",
              placeholder: t("pages.storageHooks.filters.typeAll"),
              value: typeFilter,
              onChange: setTypeFilter,
              options: [
                { value: "expression", label: t("enums.expression") },
                { value: "webhook", label: t("enums.webhook") },
                { value: "wasm", label: t("enums.wasm") },
              ],
            },
            {
              key: "enabled",
              placeholder: t("pages.storageHooks.filters.enabledAll"),
              value: enabledFilter,
              onChange: setEnabledFilter,
              options: [
                { value: "enabled", label: t("common.filterEnabled") },
                { value: "disabled", label: t("common.filterDisabled") },
              ],
            },
          ]}
        />
        <Table
          rowKey="id"
          loading={query.isFetching}
          columns={columns}
          dataSource={filteredHooks as StorageHook[]}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <StorageHookFormDrawer
        open={modalOpen}
        hook={editing}
        onClose={() => setModalOpen(false)}
      />
    </>
  );
}
