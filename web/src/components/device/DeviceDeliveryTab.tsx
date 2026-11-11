// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceDeliveryTab renders, on a sub-device's own detail page, the two
// platform surfaces its connection depends on: the single connector
// instance bound to it (matched from the owning gateway's connector set by
// device_id) and the secret bindings the gateway carries. Both are
// read-only and deep-link to the owning gateway's Connectors / Secrets
// tabs where management actually happens. The secret list shows the open
// {kind,value} selector vocabulary without interpreting it: matching is the
// edge's decision, so the table only records which bindings the gateway
// holds, never "which secrets this device receives".

import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import {
  Alert,
  Descriptions,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import dayjs from "dayjs";

import { listConnectors } from "../../api/connectors";
import {
  listSecretBindings,
  type SecretBinding,
} from "../../api/secretBindings";
import { ConnectorStateColor } from "../../utils/connectorState";
import { SecretStateColor } from "../../utils/secretState";
import { useWSStore } from "../../store/wsStore";

// formatTime renders an RFC3339 timestamp in the locale's date-time format,
// mirroring the device detail header.
function formatTime(value: string, language: string): string {
  return dayjs(value).format(
    language.startsWith("zh") ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A",
  );
}

// DeviceDeliveryTab shows the connector + secret surfaces of a sub-device.
export default function DeviceDeliveryTab({
  deviceId,
  gatewayId,
}: {
  deviceId: string;
  gatewayId: string;
}) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();

  const connectorsQuery = useQuery({
    queryKey: ["gateway-connectors", gatewayId],
    queryFn: () => listConnectors(gatewayId),
  });
  const bindingsQuery = useQuery({
    queryKey: ["gateway-secret-bindings", gatewayId],
    queryFn: () => listSecretBindings(gatewayId),
  });

  // One connector instance is bound to a sub-device at most (device_id is a
  // unique target); the header chip and this block must agree on the match.
  const connector = (connectorsQuery.data ?? []).find(
    (spec) => spec.device_id === deviceId,
  );

  // Live updates mirror the gateway tabs: connector and secret_binding
  // events naming this gateway refresh the two read-only queries.
  const subscribeWS = useWSStore((state) => state.subscribe);
  const unsubscribeWS = useWSStore((state) => state.unsubscribe);
  const connectorEvents = useWSStore((state) => state.connectorEvents);
  const secretBindingEvents = useWSStore((state) => state.secretBindingEvents);

  useEffect(() => {
    subscribeWS(`gateway-connectors-${gatewayId}`, "connector", []);
    subscribeWS(`gateway-secrets-${gatewayId}`, "secret_binding", []);
    return () => {
      unsubscribeWS(`gateway-connectors-${gatewayId}`);
      unsubscribeWS(`gateway-secrets-${gatewayId}`);
    };
  }, [gatewayId, subscribeWS, unsubscribeWS]);

  useEffect(() => {
    if (connectorEvents.length === 0) return;
    const latest = connectorEvents[connectorEvents.length - 1];
    if (latest.gateway_id === gatewayId) {
      void queryClient.invalidateQueries({
        queryKey: ["gateway-connectors", gatewayId],
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectorEvents, gatewayId]);

  useEffect(() => {
    if (secretBindingEvents.length === 0) return;
    const latest = secretBindingEvents[secretBindingEvents.length - 1];
    if (latest.gateway_id === gatewayId) {
      void queryClient.invalidateQueries({
        queryKey: ["gateway-secret-bindings", gatewayId],
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [secretBindingEvents, gatewayId]);

  const secretColumns = [
    {
      title: t("pages.deviceDetail.secrets.columns.secret"),
      dataIndex: "secret_name",
      render: (name: string) => <strong>{name}</strong>,
    },
    {
      title: t("pages.deviceDetail.secrets.columns.type"),
      render: (_: unknown, binding: SecretBinding) =>
        binding.secret_type ? (
          <Tag color="geekblue">
            {binding.secret_type}
            {binding.type_version ? `:${binding.type_version}` : ""}
          </Tag>
        ) : (
          "-"
        ),
    },
    {
      title: t("pages.deviceDetail.secrets.columns.targets"),
      render: (_: unknown, binding: SecretBinding) =>
        binding.selectors.length > 0 ? (
          <Space size={[4, 4]} wrap>
            {binding.selectors.map((selector) => (
              <Tag key={`${selector.kind}:${selector.value}`}>
                {selector.kind}: {selector.value}
              </Tag>
            ))}
          </Space>
        ) : (
          <Tag>{t("pages.deviceDetail.secrets.noTargets")}</Tag>
        ),
    },
    {
      title: t("pages.deviceDetail.secrets.columns.status"),
      render: (_: unknown, binding: SecretBinding) => {
        const tag = (
          <Tag color={SecretStateColor[binding.status] ?? "default"}>
            {t(`pages.deviceDetail.secrets.states.${binding.status}`, {
              defaultValue: binding.status,
            })}
          </Tag>
        );
        return binding.reason ? (
          <Tooltip title={binding.reason}>{tag}</Tooltip>
        ) : (
          tag
        );
      },
    },
    {
      title: t("pages.deviceDetail.secrets.columns.revision"),
      dataIndex: "revision",
    },
  ];

  return (
    <>
      <Typography.Title level={5}>
        {t("pages.deviceDetail.delivery.connectorTitle")}
      </Typography.Title>
      <Descriptions
        column={2}
        size="small"
        bordered
        style={{ marginBottom: 24 }}
      >
        {connector ? (
          <>
            <Descriptions.Item
              label={t("pages.connectors.columns.type")}
              span={2}
            >
              <Space direction="vertical" size={0}>
                <Typography.Text strong>
                  {connector.connector_type}
                </Typography.Text>
                {connector.type_version && (
                  <Typography.Text type="secondary">
                    {connector.type_version}
                  </Typography.Text>
                )}
              </Space>
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.connectors.columns.state")}>
              <Tooltip title={connector.report_reason || ""}>
                <Tag
                  color={ConnectorStateColor[connector.state] ?? "default"}
                >
                  {t(`pages.connectors.states.${connector.state}`, {
                    defaultValue: connector.state,
                  })}
                </Tag>
              </Tooltip>
            </Descriptions.Item>
            <Descriptions.Item label={t("pages.connectors.columns.revision")}>
              {connector.revision} / {connector.reported_revision}
            </Descriptions.Item>
            <Descriptions.Item
              label={t("pages.deviceDetail.delivery.lastReported")}
            >
              {connector.reported_at
                ? formatTime(connector.reported_at, i18n.language)
                : "-"}
            </Descriptions.Item>
            <Descriptions.Item span={2}>
              <Link to={`/devices/${gatewayId}?tab=connectors`}>
                {t("pages.deviceDetail.delivery.manageConnectors")}
              </Link>
            </Descriptions.Item>
          </>
        ) : (
          <Descriptions.Item span={2}>
            <Typography.Text type="secondary">
              {t("pages.deviceDetail.delivery.connectorEmpty")}
            </Typography.Text>
          </Descriptions.Item>
        )}
      </Descriptions>

      <Typography.Title level={5}>
        {t("pages.deviceDetail.delivery.secretTitle")}
      </Typography.Title>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 12 }}
        description={t("pages.deviceDetail.delivery.secretHint")}
      />
      <Table
        rowKey="id"
        loading={bindingsQuery.isLoading}
        columns={secretColumns}
        dataSource={bindingsQuery.data ?? []}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{
          emptyText: t("pages.deviceDetail.delivery.secretEmpty"),
        }}
        footer={() => (
          <Link to={`/devices/${gatewayId}?tab=secrets`}>
            {t("pages.deviceDetail.delivery.manageSecrets")}
          </Link>
        )}
      />
    </>
  );
}