// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// MonitorDrawer fetches one health snapshot when opened and on explicit
// Refresh. Optional job metrics remain undefined end to end: unavailable is a
// localized state, never a fabricated numeric zero.

import { useQuery } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Descriptions,
  Drawer,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import type { ColumnsType } from "antd/es/table";

import { getBusinessMonitor, type CameraMonitor } from "../../api/business";
import { extractError } from "../../api/client";
import ValidationIssues from "./ValidationIssues";

interface MonitorDrawerProps {
  instanceId?: string;
  instanceName?: string;
  open: boolean;
  onClose: () => void;
}

// MonitorDrawer renders instance health, per-camera job metrics, and issues.
export default function MonitorDrawer({
  instanceId,
  instanceName,
  open,
  onClose,
}: MonitorDrawerProps) {
  const { t } = useTranslation();
  const query = useQuery({
    queryKey: ["business-monitor", instanceId],
    queryFn: () => getBusinessMonitor(instanceId!),
    enabled: open && Boolean(instanceId),
    refetchInterval: false,
    refetchOnWindowFocus: false,
  });

  // metric renders a real numeric zero while localizing every absent or
  // non-finite value as unavailable.
  const metric = (value: number | undefined): string =>
    typeof value === "number" && Number.isFinite(value)
      ? value.toLocaleString()
      : t("business.monitor.unavailable");

  const columns: ColumnsType<CameraMonitor> = [
    {
      title: t("business.monitor.node"),
      dataIndex: "node_id",
      width: 180,
    },
    {
      title: t("business.monitor.device"),
      dataIndex: "device_id",
      width: 170,
    },
    {
      title: t("business.monitor.gateway"),
      dataIndex: "gateway_id",
      width: 170,
    },
    {
      title: t("business.monitor.state"),
      dataIndex: "state",
      width: 130,
      render: (state: string) => (
        <Tag>{t(`business.monitor.states.${state}`, { defaultValue: state })}</Tag>
      ),
    },
    {
      title: t("business.monitor.lastError"),
      dataIndex: "last_error",
      width: 220,
      render: (lastError: string | undefined) =>
        lastError?.trim() ? (
          <Typography.Text type="danger">{lastError}</Typography.Text>
        ) : (
          <Typography.Text type="secondary">
            {t("business.monitor.noError")}
          </Typography.Text>
        ),
    },
    {
      title: t("business.monitor.p50Ms"),
      dataIndex: "p50_ms",
      width: 110,
      align: "right",
      render: metric,
    },
    {
      title: t("business.monitor.p95Ms"),
      dataIndex: "p95_ms",
      width: 110,
      align: "right",
      render: metric,
    },
    {
      title: t("business.monitor.eventsPerSecond"),
      dataIndex: "events_per_second",
      width: 150,
      align: "right",
      render: metric,
    },
    {
      title: t("business.monitor.successTotal"),
      dataIndex: "success_total",
      width: 130,
      align: "right",
      render: metric,
    },
    {
      title: t("business.monitor.errorTotal"),
      dataIndex: "error_total",
      width: 120,
      align: "right",
      render: metric,
    },
    {
      title: t("business.monitor.healthSource"),
      dataIndex: "health_source",
      width: 150,
    },
  ];

  const monitor = query.data;
  return (
    <Drawer
      open={open}
      onClose={onClose}
      width={1000}
      title={t("business.monitor.title", { name: instanceName ?? instanceId ?? "" })}
      extra={
        <Button
          icon={<ReloadOutlined />}
          loading={query.isFetching}
          disabled={!instanceId}
          onClick={() => void query.refetch()}
        >
          {t("business.actions.refresh")}
        </Button>
      }
    >
      {query.error ? (
        <Alert
          type="error"
          showIcon
          message={t("business.monitor.loadFailed")}
          description={extractError(query.error).message}
        />
      ) : (
        <Space direction="vertical" size={20} style={{ width: "100%" }}>
          <Descriptions
            bordered
            size="small"
            column={{ xs: 1, sm: 2, md: 4 }}
            items={[
              {
                key: "status",
                label: t("business.instances.status"),
                children: monitor ? (
                  <Tag color={monitor.status === "RUNNING" ? "success" : "processing"}>
                    {t(`business.statuses.${monitor.status}`, {
                      defaultValue: monitor.status,
                    })}
                  </Tag>
                ) : (
                  t("business.monitor.unavailable")
                ),
              },
              {
                key: "generation",
                label: t("business.instances.generation"),
                children: monitor?.generation ?? t("business.monitor.unavailable"),
              },
              {
                key: "activeGeneration",
                label: t("business.instances.activeGeneration"),
                children: monitor?.active_generation ?? t("business.monitor.unavailable"),
              },
              {
                key: "instance",
                label: t("business.monitor.instanceId"),
                children: monitor?.instance_id ?? instanceId,
              },
            ]}
          />

          <section aria-label={t("business.monitor.cameras")}>
            <Typography.Title level={5}>{t("business.monitor.cameras")}</Typography.Title>
            <Table<CameraMonitor>
              rowKey="node_id"
              loading={query.isLoading || query.isFetching}
              columns={columns}
              dataSource={monitor?.cameras ?? []}
              scroll={{ x: 1500 }}
              pagination={false}
              locale={{ emptyText: t("business.monitor.noCameras") }}
            />
          </section>

          <section aria-label={t("business.monitor.issues")}>
            <Typography.Title level={5}>{t("business.monitor.issues")}</Typography.Title>
            <ValidationIssues issues={monitor?.issues ?? []} />
          </section>
        </Space>
      )}
    </Drawer>
  );
}
