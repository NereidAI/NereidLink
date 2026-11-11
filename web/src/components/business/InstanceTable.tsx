// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// InstanceTable is the operational inventory for saved business deployments.
// It exposes explicit edit, monitor, refresh, and delete actions; lifecycle
// generation changes remain generation-fenced in the canvas workflow.

import {
  Alert,
  Button,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  DeleteOutlined,
  EditOutlined,
  FundViewOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";

import type { BusinessInstance } from "../../api/business";
import { extractError } from "../../api/client";
import { formatDateTime } from "../../utils/format";

interface InstanceTableProps {
  instances: BusinessInstance[];
  loading: boolean;
  error?: unknown;
  deletingId?: string;
  onRefresh: () => void;
  onEdit: (instance: BusinessInstance) => void;
  onMonitor: (instance: BusinessInstance) => void;
  onDelete: (instance: BusinessInstance) => void;
}

// statusColor maps known lifecycle states to visual cues while leaving the
// localized status text as the accessible source of meaning.
function statusColor(status: string): string {
  if (status === "RUNNING") return "success";
  if (status === "FAILED" || status === "DEGRADED") return "error";
  if (status === "DEPLOYING" || status === "VALIDATING") return "processing";
  return "default";
}

// InstanceTable renders desired/active generations and on-demand actions.
export default function InstanceTable({
  instances,
  loading,
  error,
  deletingId,
  onRefresh,
  onEdit,
  onMonitor,
  onDelete,
}: InstanceTableProps) {
  const { t } = useTranslation();
  const columns: ColumnsType<BusinessInstance> = [
    {
      title: t("business.instances.name"),
      dataIndex: "name",
      width: 220,
      render: (name: string, instance) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{name}</Typography.Text>
          <Typography.Text type="secondary" copyable={{ text: instance.id }}>
            {instance.id}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t("business.instances.status"),
      dataIndex: "status",
      width: 140,
      render: (status: string) => (
        <Tag color={statusColor(status)}>
          {t(`business.statuses.${status}`, { defaultValue: status })}
        </Tag>
      ),
    },
    {
      title: t("business.instances.generation"),
      dataIndex: "generation",
      width: 130,
      align: "right",
    },
    {
      title: t("business.instances.activeGeneration"),
      dataIndex: "active_generation",
      width: 160,
      align: "right",
      render: (generation: number) =>
        generation > 0 ? generation : t("business.instances.noActiveGeneration"),
    },
    {
      title: t("business.instances.pendingGeneration"),
      dataIndex: "pending_generation",
      width: 160,
      align: "right",
      render: (generation: number) =>
        generation > 0 ? generation : t("business.instances.noPendingGeneration"),
    },
    {
      title: t("business.instances.template"),
      key: "template",
      width: 260,
      render: (_, instance) => `${instance.template_id}@${instance.template_version}`,
    },
    {
      title: t("business.instances.updatedAt"),
      dataIndex: "updated_at",
      width: 200,
      render: (updatedAt: string) => formatDateTime(updatedAt),
    },
    {
      title: t("business.instances.actions"),
      key: "actions",
      fixed: "right",
      width: 170,
      render: (_, instance) => (
        <Space size={2}>
          <Tooltip title={t("business.instances.edit")}>
            <Button
              type="text"
              icon={<EditOutlined />}
              aria-label={t("business.instances.editNamed", { name: instance.name })}
              onClick={() => onEdit(instance)}
            />
          </Tooltip>
          <Tooltip title={t("business.instances.monitor")}>
            <Button
              type="text"
              icon={<FundViewOutlined />}
              aria-label={t("business.instances.monitorNamed", { name: instance.name })}
              onClick={() => onMonitor(instance)}
            />
          </Tooltip>
          <Popconfirm
            title={t("business.instances.deleteTitle")}
            description={t("business.instances.deleteDescription", {
              name: instance.name,
            })}
            okText={t("business.actions.delete")}
            cancelText={t("business.actions.cancel")}
            okButtonProps={{ danger: true }}
            onConfirm={() => onDelete(instance)}
          >
            <Tooltip title={t("business.actions.delete")}>
              <Button
                type="text"
                danger
                loading={deletingId === instance.id}
                icon={<DeleteOutlined />}
                aria-label={t("business.instances.deleteNamed", { name: instance.name })}
              />
            </Tooltip>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <>
      <Space style={{ width: "100%", justifyContent: "flex-end", marginBottom: 16 }}>
        <Button icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
          {t("business.actions.refresh")}
        </Button>
      </Space>
      {error && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("business.instances.loadFailed")}
          description={extractError(error).message}
        />
      )}
      <Table<BusinessInstance>
        rowKey="id"
        loading={loading}
        columns={columns}
        dataSource={instances}
        scroll={{ x: 1350 }}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("business.instances.total", { total }),
        }}
        locale={{ emptyText: t("business.instances.empty") }}
      />
    </>
  );
}
