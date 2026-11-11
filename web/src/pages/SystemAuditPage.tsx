// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SystemAuditPage is the platform console for the tamper-evident audit
// chain (ADR-0029): filter by event type, tenant or actor, inspect rows
// and recompute the hash chain on demand. It is platform-admin only.

import { useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Input,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { SafetyCertificateOutlined } from "@ant-design/icons";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import { useDebounce } from "../hooks/useDebounce";
import { extractError } from "../api/client";
import { authStore } from "../store/authStore";
import {
  listAudit,
  verifyAudit,
  type AuditEvent,
} from "../api/system";

// pageSize is the fixed audit page length.
const pageSize = 20;

// SystemAuditPage renders the audit chain console.
export default function SystemAuditPage() {
  const { t } = useTranslation();
  const user = authStore((state) => state.user);
  const { message } = AntApp.useApp();

  // Draft inputs feed the bar; the debounced copy drives the query so
  // typing does not fire a request per keystroke.
  const [draft, setDraft] = useState({ type: "", tenant: "", actor: "" });
  const filters = useDebounce(draft, 300);
  const [page, setPage] = useState(1);

  const auditQuery = useQuery({
    queryKey: ["system-audit", filters, page],
    queryFn: () =>
      listAudit({
        type: filters.type || undefined,
        tenant: filters.tenant || undefined,
        actor: filters.actor || undefined,
        page,
        page_size: pageSize,
      }),
  });

  const verifyMutation = useMutation({
    mutationFn: verifyAudit,
    onError: (error) => message.error(extractError(error).message),
  });

  if (!user?.is_platform_admin) {
    return (
      <>
        <PageHeader title={t("pages.systemAudit.title")} />
        <Alert type="error" showIcon message={t("pages.systemAudit.forbidden")} />
      </>
    );
  }

  const columns = [
    {
      title: t("pages.systemAudit.columns.seq"),
      dataIndex: "seq",
      width: 90,
    },
    {
      title: t("pages.systemAudit.columns.time"),
      dataIndex: "time",
      width: 210,
      render: (value: string) => (
        <Typography.Text type="secondary">{value}</Typography.Text>
      ),
    },
    {
      title: t("pages.systemAudit.columns.actor"),
      key: "actor",
      render: (_: unknown, row: AuditEvent) =>
        row.actor_name || row.actor_id || "-",
    },
    {
      title: t("pages.systemAudit.columns.event"),
      key: "event",
      render: (_: unknown, row: AuditEvent) => (
        <Space size={4} wrap>
          <Tag>{row.event_type}</Tag>
          <Typography.Text>{row.action}</Typography.Text>
        </Space>
      ),
    },
    {
      title: t("pages.systemAudit.columns.resource"),
      key: "resource",
      render: (_: unknown, row: AuditEvent) => (
        <Space size={4} direction="vertical" style={{ gap: 0 }}>
          <Typography.Text type="secondary">{row.resource_type}</Typography.Text>
          <code>{row.resource_id || "-"}</code>
        </Space>
      ),
    },
    {
      title: t("pages.systemAudit.columns.ip"),
      dataIndex: "ip",
      width: 140,
    },
  ];

  const verifyResult = verifyMutation.data;

  return (
    <>
      <PageHeader
        title={t("pages.systemAudit.title")}
        subtitle={t("pages.systemAudit.subtitle")}
        helpId="system-audit"
        extra={
          <Button
            icon={<SafetyCertificateOutlined />}
            loading={verifyMutation.isPending}
            onClick={() => verifyMutation.mutate()}
          >
            {t("pages.systemAudit.verify")}
          </Button>
        }
      />
      {verifyResult && (
        <Alert
          style={{ marginBottom: 16 }}
          showIcon
          type={verifyResult.ok ? "success" : "error"}
          message={
            verifyResult.ok
              ? t("pages.systemAudit.verifyOk", {
                  events: verifyResult.events_checked,
                  seals: verifyResult.seals_checked,
                })
              : t("pages.systemAudit.verifyFailed", {
                  seq: verifyResult.broken_at_seq,
                  reason: verifyResult.reason,
                })
          }
        />
      )}
      <FilterSearchBar
        searchValue={draft.type}
        searchPlaceholder={t("pages.systemAudit.filters.typePlaceholder")}
        onSearchChange={(value) => {
          setDraft((previous) => ({ ...previous, type: value }));
          setPage(1);
        }}
        extra={
          <>
            <Input
              allowClear
              style={{ width: 180 }}
              value={draft.tenant}
              placeholder={t("pages.systemAudit.filters.tenantPlaceholder")}
              onChange={(event) => {
                setDraft((previous) => ({
                  ...previous,
                  tenant: event.target.value,
                }));
                setPage(1);
              }}
            />
            <Input
              allowClear
              style={{ width: 180 }}
              value={draft.actor}
              placeholder={t("pages.systemAudit.filters.actorPlaceholder")}
              onChange={(event) => {
                setDraft((previous) => ({
                  ...previous,
                  actor: event.target.value,
                }));
                setPage(1);
              }}
            />
          </>
        }
      />
      <Card>
        <Table
          rowKey="seq"
          loading={auditQuery.isFetching}
          columns={columns}
          dataSource={auditQuery.data?.events ?? []}
          pagination={{
            current: page,
            pageSize,
            total: auditQuery.data?.total ?? 0,
            showSizeChanger: false,
            showTotal: (total) => t("common.totalItems", { total }),
            onChange: setPage,
          }}
          expandable={{
            rowExpandable: (row) => !!row.details,
            expandedRowRender: (row) => (
              <pre style={{ margin: 0, whiteSpace: "pre-wrap" }}>
                {JSON.stringify(row.details, null, 2)}
              </pre>
            ),
          }}
        />
      </Card>
    </>
  );
}
