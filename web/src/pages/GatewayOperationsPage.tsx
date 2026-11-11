// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// GatewayOperationsPage lists the effective gateway operation catalog:
// risk-grouped rows, disable switches, clone for edge descriptors and
// edit/delete for private ones. No invocation UI in this ticket.

import {
  Badge,
  Button,
  Popconfirm,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from "antd";
import {
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import OperationDrawer from "../components/OperationDrawer";
import OperationDetailDrawer from "../components/OperationDetailDrawer";
import {
  cloneOperation,
  deleteOperation,
  listOperations,
  setOperationDisabled,
  type OperationView,
} from "../api/gatewayOperations";

// riskColor maps risk levels to tag colors.
function riskColor(risk: string): string {
  if (risk === "dangerous") return "red";
  if (risk === "read") return "blue";
  return "default";
}

// localizedLabel renders a {en,zh} map in the active UI language, falling
// back to English and then the provided key.
function localizedLabel(map: Record<string, string> | undefined, lang: string, fallback: string): string {
  const key = lang.startsWith("zh") ? "zh" : "en";
  return map?.[key] || map?.en || fallback;
}

// GatewayOperationsPage renders the catalog.
export default function GatewayOperationsPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [target, setTarget] = useState<OperationView | null>(null);
  const [detail, setDetail] = useState<OperationView | null>(null);

  const { data: items = [], isLoading } = useQuery({
    queryKey: ["gateway-operations"],
    queryFn: listOperations,
  });

  const [keyword, setKeyword] = useState("");
  const [sourceFilter, setSourceFilter] = useState("");
  const [riskFilter, setRiskFilter] = useState("");

  // filtered applies the keyword and dropdown filters client-side. The
  // catalog originates from one bounded endpoint, so no pagination is
  // needed on the server; the table pages locally below.
  const filtered = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return items.filter((row) => {
      if (sourceFilter && row.source !== sourceFilter) return false;
      if (riskFilter && row.risk !== riskFilter) return false;
      if (!needle) return true;
      const haystack = [
        row.operation,
        row.type_version,
        row.display_name?.en ?? "",
        row.display_name?.zh ?? "",
        row.description?.en ?? "",
        row.description?.zh ?? "",
      ]
        .join("\n")
        .toLowerCase();
      return haystack.includes(needle);
    });
  }, [items, keyword, sourceFilter, riskFilter]);

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["gateway-operations"] });

  const disableMutation = useMutation({
    mutationFn: ({ row, disabled }: { row: OperationView; disabled: boolean }) =>
      setOperationDisabled(row.operation, row.type_version, disabled),
    onSuccess: refresh,
  });

  const cloneMutation = useMutation({
    mutationFn: (row: OperationView) =>
      cloneOperation(row.operation, row.type_version, `${row.operation}-copy`),
    onSuccess: refresh,
  });

  const deleteMutation = useMutation({
    mutationFn: (row: OperationView) =>
      deleteOperation(row.operation, row.type_version),
    onSuccess: refresh,
  });

  const openCreate = () => {
    setTarget(null);
    setDrawerOpen(true);
  };

  const openEdit = (row: OperationView) => {
    setTarget(row);
    setDrawerOpen(true);
  };

  const openEditFromDetail = (row: OperationView) => {
    setDetail(null);
    openEdit(row);
  };

  const columns = [
    {
      title: t("pages.operations.table.operation"),
      dataIndex: "operation",
      render: (_: string, row: OperationView) => (
        <Space direction="vertical" size={0}>
          <Space size={6} wrap>
            <strong>{localizedLabel(row.display_name, i18n.language, row.operation)}</strong>
            {row.conflict && (
              <Tooltip title={t("pages.operations.conflictHint")}>
                <Tag color="red" style={{ marginInlineEnd: 0 }}>
                  {t("pages.operations.conflict")}
                </Tag>
              </Tooltip>
            )}
          </Space>
          <span>{row.operation}</span>
          {(() => {
            const desc = localizedLabel(row.description, i18n.language, "");
            return desc ? (
              <span style={{ color: "rgba(0,0,0,0.45)" }}>{desc}</span>
            ) : null;
          })()}
          {row.source === "edge" && (row.gateway_count ?? 0) > 0 && (
            <Tooltip
              title={(row.advertised_by ?? []).map((g) => g.name).join(", ")}
            >
              <span style={{ color: "rgba(0,0,0,0.45)", fontSize: 12 }}>
                {t("pages.operations.advertisedBy", { count: row.gateway_count })}
              </span>
            </Tooltip>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.operations.table.risk"),
      dataIndex: "risk",
      width: 110,
      render: (risk: string) => <Tag color={riskColor(risk)}>{t(`pages.operations.risks.${risk}`)}</Tag>,
    },
    {
      title: t("pages.operations.table.interaction"),
      dataIndex: "interaction",
      width: 130,
      render: (interaction: string) =>
        t(interaction === "one-way" ? "pages.operations.interaction.ow" : "pages.operations.interaction.rr"),
    },
    {
      title: t("pages.operations.table.timeout"),
      dataIndex: "timeout_ms",
      width: 100,
      render: (ms: number) => t("pages.operations.table.timeoutMs", { ms }),
    },
    {
      title: t("pages.operations.table.source"),
      dataIndex: "source",
      width: 100,
      render: (source: string) =>
        t(source === "edge" ? "pages.operations.sources.edge" : "pages.operations.sources.manual"),
    },
    {
      title: t("pages.operations.table.enabled"),
      dataIndex: "disabled",
      width: 100,
      render: (_: boolean, row: OperationView) => (
        <Badge status={row.disabled ? "error" : "success"} text={
          <Switch
            size="small"
            checked={!row.disabled}
            onChange={(checked) => disableMutation.mutate({ row, disabled: !checked })}
          />
        } />
      ),
    },
    {
      title: "",
      width: 150,
      render: (_: unknown, row: OperationView) => (
        <Space>
          <Tooltip title={t("common.view")}>
            <Button type="text" icon={<EyeOutlined />} onClick={() => setDetail(row)} />
          </Tooltip>
          {row.source === "edge" ? (
            <Tooltip title={t("pages.operations.clone")}>
              <Button type="text" icon={<CopyOutlined />} onClick={() => cloneMutation.mutate(row)} />
            </Tooltip>
          ) : (
            <>
              <Tooltip title={t("common.edit")}>
                <Button type="text" icon={<EditOutlined />} onClick={() => openEdit(row)} />
              </Tooltip>
              <Popconfirm
                title={t("pages.operations.confirmDelete", { name: row.operation })}
                okText={t("common.delete")}
                cancelText={t("common.cancel")}
                okButtonProps={{ danger: true }}
                onConfirm={() => deleteMutation.mutate(row)}
              >
                <Button type="text" danger icon={<DeleteOutlined />} />
              </Popconfirm>
            </>
          )}
        </Space>
      ),
    },
  ];

  return (
    <div>
      <PageHeader
        title={t("nav.operations")}
        helpId="gateway-operations"
        extra={
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            {t("pages.operations.add")}
          </Button>
        }
      />
      <div style={{ position: "relative", minHeight: 40, marginBottom: 12 }}>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.operations.searchPlaceholder")}
          filters={[
            {
              key: "source",
              placeholder: t("pages.operations.filters.sourceAll"),
              value: sourceFilter,
              onChange: setSourceFilter,
              options: [
                { value: "edge", label: t("pages.operations.sources.edge") },
                { value: "manual", label: t("pages.operations.sources.manual") },
              ],
            },
            {
              key: "risk",
              placeholder: t("pages.operations.filters.riskAll"),
              value: riskFilter,
              onChange: setRiskFilter,
              options: [
                { value: "read", label: t("pages.operations.risks.read") },
                { value: "normal", label: t("pages.operations.risks.normal") },
                { value: "dangerous", label: t("pages.operations.risks.dangerous") },
              ],
            },
          ]}
        />
      </div>
      <Table
        rowKey={(row) => `${row.operation}|${row.type_version}`}
        loading={isLoading}
        dataSource={filtered}
        columns={columns}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      <OperationDrawer
        open={drawerOpen}
        target={target}
        onClose={() => setDrawerOpen(false)}
        onSaved={refresh}
      />
      <OperationDetailDrawer
        open={detail !== null}
        target={detail}
        onClose={() => setDetail(null)}
        onEdit={openEditFromDetail}
        onCloned={() => setDetail(null)}
      />
    </div>
  );
}
