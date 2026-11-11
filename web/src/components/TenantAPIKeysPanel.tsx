// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// TenantAPIKeysPanel gives tenant administrators cross-user visibility and
// revocation over long-lived user API keys. Key creation stays on Account
// Security because a key always belongs to the human who creates it.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";
import dayjs from "dayjs";

import {
  deleteTenantAPIKey,
  listTenantAPIKeys,
  revokeTenantAPIKey,
  type APIKey,
} from "../api/apiKeys";
import { listMembers, type Member } from "../api/system";
import { extractError } from "../api/client";
import FilterSearchBar from "./FilterSearchBar";
import { formatDateTime } from "../utils/format";

interface TenantAPIKeysPanelProps {
  tenantId: string;
}

export default function TenantAPIKeysPanel({
  tenantId,
}: TenantAPIKeysPanelProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const [keyword, setKeyword] = useState("");
  const [statusFilter, setStatusFilter] = useState("");

  const keysQuery = useQuery({
    queryKey: ["tenant-api-keys", tenantId],
    queryFn: () => listTenantAPIKeys(tenantId),
    enabled: !!tenantId,
  });
  const membersQuery = useQuery({
    queryKey: ["members", tenantId],
    queryFn: () => listMembers(tenantId),
    enabled: !!tenantId,
  });

  const membersById = useMemo(() => {
    const map = new Map<string, Member>();
    for (const member of membersQuery.data ?? []) {
      map.set(member.user_id, member);
    }
    return map;
  }, [membersQuery.data]);

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: ["tenant-api-keys", tenantId],
    });

  const revokeMutation = useMutation({
    mutationFn: (key: APIKey) => revokeTenantAPIKey(tenantId, key.id),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.systemUsers.apiKeys.revoked"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: (key: APIKey) => deleteTenantAPIKey(tenantId, key.id),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.systemUsers.apiKeys.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const filteredKeys = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return (keysQuery.data ?? []).filter((key) => {
      const member = membersById.get(key.user_id);
      const owner = `${member?.display_name ?? ""} ${member?.email ?? ""} ${key.user_id}`.toLowerCase();
      const expired = Boolean(
        key.expires_at && dayjs(key.expires_at).isBefore(dayjs()),
      );
      if (statusFilter === "active" && (key.revoked || expired)) return false;
      if (statusFilter === "revoked" && !key.revoked) return false;
      if (statusFilter === "expired" && (!expired || key.revoked)) return false;
      if (!needle) return true;
      return (
        key.name.toLowerCase().includes(needle) ||
        key.key_prefix.toLowerCase().includes(needle) ||
        owner.includes(needle)
      );
    });
  }, [keysQuery.data, keyword, membersById, statusFilter]);

  const columns: ColumnsType<APIKey> = [
    {
      title: t("pages.systemUsers.apiKeys.owner"),
      key: "owner",
      render: (_, record) => {
        const member = membersById.get(record.user_id);
        return (
          <Space direction="vertical" size={0}>
            <Typography.Text>{member?.display_name ?? record.user_id}</Typography.Text>
            {member?.email && (
              <Typography.Text type="secondary">{member.email}</Typography.Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.accountSecurity.apiKeys.name"),
      dataIndex: "name",
      key: "name",
    },
    {
      title: t("pages.accountSecurity.apiKeys.prefix"),
      dataIndex: "key_prefix",
      key: "key_prefix",
      render: (prefix: string) => <Typography.Text code>{prefix}</Typography.Text>,
    },
    {
      title: t("pages.accountSecurity.apiKeys.scopes"),
      dataIndex: "scopes",
      key: "scopes",
      render: (scopes: string[]) =>
        scopes.length === 0 ? (
          <Tag>{t("pages.accountSecurity.apiKeys.inherit")}</Tag>
        ) : (
          scopes.map((scope) => <Tag key={scope}>{scope}</Tag>)
        ),
    },
    {
      title: t("pages.accountSecurity.apiKeys.status"),
      key: "status",
      render: (_, record) => {
        const expired = Boolean(
          record.expires_at && dayjs(record.expires_at).isBefore(dayjs()),
        );
        if (record.revoked) {
          return <Tag color="red">{t("pages.accountSecurity.apiKeys.revoked")}</Tag>;
        }
        if (expired) {
          return <Tag color="orange">{t("pages.accountSecurity.apiKeys.expired")}</Tag>;
        }
        return <Tag color="green">{t("pages.accountSecurity.apiKeys.active")}</Tag>;
      },
    },
    {
      title: t("pages.accountSecurity.apiKeys.lastUsed"),
      dataIndex: "last_used_at",
      key: "last_used_at",
      render: (value?: string | null) =>
        value ? formatDateTime(value) : t("pages.accountSecurity.apiKeys.neverUsed"),
    },
    {
      title: t("pages.accountSecurity.apiKeys.actions"),
      key: "actions",
      render: (_, record) => (
        <Space>
          <Popconfirm
            title={t("pages.systemUsers.apiKeys.revokeConfirm")}
            onConfirm={() => revokeMutation.mutateAsync(record)}
          >
            <Button size="small" danger disabled={record.revoked}>
              {t("pages.accountSecurity.apiKeys.revoke")}
            </Button>
          </Popconfirm>
          <Popconfirm
            title={t("pages.systemUsers.apiKeys.deleteConfirm")}
            onConfirm={() => deleteMutation.mutateAsync(record)}
          >
            <Button size="small" danger type="primary" ghost>
              {t("common.delete")}
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <>
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t("pages.systemUsers.apiKeys.searchPlaceholder")}
        filters={[
          {
            key: "status",
            placeholder: t("pages.systemUsers.apiKeys.statusAll"),
            value: statusFilter,
            onChange: setStatusFilter,
            options: [
              {
                value: "active",
                label: t("pages.accountSecurity.apiKeys.active"),
              },
              {
                value: "revoked",
                label: t("pages.accountSecurity.apiKeys.revoked"),
              },
              {
                value: "expired",
                label: t("pages.accountSecurity.apiKeys.expired"),
              },
            ],
          },
        ]}
      />
      <Table
        rowKey="id"
        size="small"
        loading={keysQuery.isLoading || membersQuery.isLoading}
        columns={columns}
        dataSource={filteredKeys}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("common.totalItems", { total }),
        }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
    </>
  );
}
