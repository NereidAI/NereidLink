// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// EscalationsPage is the alarm escalation chain list: header, card with
// table and the create/edit modal. Chains are tenant-scoped configuration
// and change only through user mutations.

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
import EscalationChainFormDrawer from "../components/EscalationChainFormDrawer";
import {
  deleteEscalationChain,
  listEscalationChains,
  type EscalationChain,
} from "../api/escalations";

// EscalationsPage renders the escalation chain management page.
export default function EscalationsPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<EscalationChain | undefined>();
  const [keyword, setKeyword] = useState("");
  const [enabledFilter, setEnabledFilter] = useState("");

  const query = useQuery({
    queryKey: ["escalation-chains"],
    queryFn: listEscalationChains,
  });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const chains = useMemo(() => query.data ?? [], [query.data]);

  // filteredChains applies client-side keyword search over the chain name
  // plus an enabled/disabled enum filter; empty filter means all.
  const filteredChains = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return chains.filter((chain) => {
      if (enabledFilter && (enabledFilter === "enabled") !== chain.enabled) {
        return false;
      }
      if (!needle) return true;
      return chain.name.toLowerCase().includes(needle);
    });
  }, [chains, keyword, enabledFilter]);

  const deleteMutation = useMutation({
    mutationFn: deleteEscalationChain,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["escalation-chains"] });
      message.success(t("pages.escalations.toasts.deleted"));
    },
    onError: () => message.error(t("pages.escalations.toasts.deleteFailed")),
  });

  const columns = [
    { title: t("common.name"), dataIndex: "name" },
    {
      title: t("pages.escalations.steps"),
      dataIndex: "steps",
      render: (steps: EscalationChain["steps"]) => (
        <Space size={4} wrap>
          {steps.map((step) => (
            <Tag key={step.step_id ?? step.order}>
              {step.delay_seconds}s → {step.target_role}
            </Tag>
          ))}
        </Space>
      ),
    },
    {
      title: t("common.enabled"),
      dataIndex: "enabled",
      width: 90,
      render: (enabled: boolean) => <Switch checked={enabled} size="small" disabled />,
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 100,
      render: (_: unknown, chain: EscalationChain) => (
        <Space>
          <Tooltip title={t("common.edit")}>
            <Button
              type="text"
              size="small"
              icon={<EditOutlined />}
              onClick={() => {
                setEditing(chain);
                setModalOpen(true);
              }}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.escalations.confirm.title")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMutation.mutate(chain.chain_id)}
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
        title={t("pages.escalations.title")}
        subtitle={t("pages.escalations.subtitle")}
        helpId="escalation-chains"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              setEditing(undefined);
              setModalOpen(true);
            }}
          >
            {t("pages.escalations.create")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.escalations.filters.searchPlaceholder")}
          filters={[
            {
              key: "enabled",
              placeholder: t("pages.escalations.filters.enabledAll"),
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
          rowKey="chain_id"
          loading={query.isFetching}
          columns={columns}
          dataSource={filteredChains}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <EscalationChainFormDrawer
        open={modalOpen}
        chain={editing}
        onClose={() => setModalOpen(false)}
      />
    </>
  );
}
