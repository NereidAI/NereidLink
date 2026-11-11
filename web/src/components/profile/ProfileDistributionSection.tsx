// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileDistributionSection is the profile-detail card that distributes a
// tenant library profile to gateways that did not report it (ADR-0038, the
// shared-library distribution main path: gateway A reported A1, gateway B needs
// A1). It manages the explicit target set and shows delivery state across
// every gateway the profile reaches, including gateways that only hold it
// implicitly through a connector instance (those rows are read-only: removing
// the explicit edge is the only withdrawal the profile owns). The profile
// source text is never edited here; the platform references the library entry
// and renders it per the gateway's declared format.

import { useMemo, useState } from "react";
import { App as AntApp, Button, Card, Modal, Popconfirm, Select, Space, Table, Tag, Tooltip, Typography } from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import dayjs from "dayjs";

import type { Profile } from "../../api/profiles";
import {
  listProfileTargets,
  distributeProfile,
  withdrawProfileTarget,
  listDeliveriesForProfile,
  type ProfileDelivery,
} from "../../api/profileDelivery";
import { listGatewayDevices } from "../../api/devices";
import { extractError } from "../../api/client";
import ProfileDeliveryStateTag from "./ProfileDeliveryStateTag";

// DistributionRow joins an explicit target with its delivery state.
interface DistributionRow {
  gatewayId: string;
  explicit: boolean;
  createdAt?: string;
  delivery?: ProfileDelivery;
}

// formatTime renders an RFC3339 timestamp in the locale's date-time format.
function formatTime(value: string, language: string): string {
  return dayjs(value).format(
    language.startsWith("zh") ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A",
  );
}

interface ProfileDistributionSectionProps {
  // profile is the library profile being distributed.
  profile: Profile;
}

// ProfileDistributionSection renders the distribution card.
export function ProfileDistributionSection({
  profile,
}: ProfileDistributionSectionProps) {
  const { t, i18n } = useTranslation();
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();

  const targetsKey = ["profile-targets", profile.id] as const;
  const deliveriesKey = ["profile-deliveries-for-profile", profile.id] as const;

  const targetsQuery = useQuery({
    queryKey: targetsKey,
    queryFn: () => listProfileTargets(profile.id),
  });
  const deliveriesQuery = useQuery({
    queryKey: deliveriesKey,
    queryFn: () => listDeliveriesForProfile(profile.id),
  });
  const gatewaysQuery = useQuery({
    queryKey: ["gateway-devices"],
    queryFn: listGatewayDevices,
  });

  const targets = targetsQuery.data ?? [];
  const gateways = gatewaysQuery.data ?? [];

  const explicitIds = useMemo(
    () =>
      new Set((targetsQuery.data ?? []).map((target) => target.gateway_id)),
    [targetsQuery.data],
  );

  // Gateway names resolve from the device list; fall back to the target join
  // name and finally the id (Delivery rows carry no gateway name).
  const gatewayName = (gatewayId: string): string =>
    gateways.find((gateway) => gateway.id === gatewayId)?.canonical_name ??
    targets.find((target) => target.gateway_id === gatewayId)?.gateway_name ??
    gatewayId;

  // Union of explicit targets and delivery rows; implicit (connector-only)
  // gateways appear but cannot be withdrawn from this surface. Query data
  // references are stable, so depending on them avoids rebuilding on render.
  const rows = useMemo<DistributionRow[]>(() => {
    const targetList = targetsQuery.data ?? [];
    const deliveryList = deliveriesQuery.data ?? [];
    const gatewayList = gatewaysQuery.data ?? [];
    const nameOf = (id: string): string =>
      gatewayList.find((gateway) => gateway.id === id)?.canonical_name ??
      targetList.find((target) => target.gateway_id === id)?.gateway_name ??
      id;
    const out: DistributionRow[] = targetList.map((target) => ({
      gatewayId: target.gateway_id,
      explicit: true,
      createdAt: target.created_at,
      delivery: deliveryList.find(
        (delivery) => delivery.gateway_id === target.gateway_id,
      ),
    }));
    deliveryList.forEach((delivery) => {
      if (!explicitIds.has(delivery.gateway_id)) {
        out.push({ gatewayId: delivery.gateway_id, explicit: false, delivery });
      }
    });
    out.sort(
      (a, b) =>
        Number(b.explicit) - Number(a.explicit) ||
        nameOf(a.gatewayId).localeCompare(nameOf(b.gatewayId)),
    );
    return out;
  }, [
    targetsQuery.data,
    deliveriesQuery.data,
    gatewaysQuery.data,
    explicitIds,
  ]);

  const invalidate = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: targetsKey }),
      queryClient.invalidateQueries({ queryKey: deliveriesKey }),
    ]);

  const [modalOpen, setModalOpen] = useState(false);
  const [selectedGateway, setSelectedGateway] = useState<string | undefined>();

  const distributeMutation = useMutation({
    mutationFn: (gatewayId: string) =>
      distributeProfile(profile.id, gatewayId),
    onSuccess: async () => {
      message.success(t("pages.profiles.distribution.successDistribute"));
      setModalOpen(false);
      setSelectedGateway(undefined);
      await invalidate();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const withdrawMutation = useMutation({
    mutationFn: (gatewayId: string) =>
      withdrawProfileTarget(profile.id, gatewayId),
    onSuccess: async () => {
      message.success(t("pages.profiles.distribution.successWithdraw"));
      await invalidate();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // Only gateways without an explicit edge are offered; implicit-only
  // gateways can still be turned into an explicit distribution target.
  const gatewayOptions = useMemo(
    () =>
      (gatewaysQuery.data ?? [])
        .filter((gateway) => !explicitIds.has(gateway.id))
        .map((gateway) => {
          const supports = (gateway.agent_features ?? []).includes("profiles");
          return {
            value: gateway.id,
            label: supports
              ? gateway.canonical_name
              : `${gateway.canonical_name} (${t("pages.profiles.distribution.unsupportedOption")})`,
          };
        }),
    [gatewaysQuery.data, explicitIds, t],
  );

  const columns = [
    {
      title: t("pages.profiles.distribution.columns.gateway"),
      render: (_: unknown, row: DistributionRow) => {
        const gateway = gateways.find((item) => item.id === row.gatewayId);
        return (
          <Space direction="vertical" size={0}>
            <Link to={`/devices/${row.gatewayId}`}>
              <strong>{gatewayName(row.gatewayId)}</strong>
            </Link>
            {gateway?.alias && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {gateway.alias}
              </Typography.Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.profiles.distribution.columns.source"),
      render: (_: unknown, row: DistributionRow) =>
        row.explicit ? (
          <Tag color="blue">{t("pages.profiles.distribution.sourceExplicit")}</Tag>
        ) : (
          <Tooltip title={t("pages.profiles.distribution.sourceImplicitTip")}>
            <Tag>{t("pages.profiles.distribution.sourceImplicit")}</Tag>
          </Tooltip>
        ),
    },
    {
      title: t("pages.profiles.distribution.columns.status"),
      render: (_: unknown, row: DistributionRow) =>
        row.delivery ? (
          <ProfileDeliveryStateTag
            state={row.delivery.state}
            reason={row.delivery.reason}
          />
        ) : (
          "-"
        ),
    },
    {
      title: (
        <Tooltip title={t("pages.profiles.distribution.revisionTip")}>
          {t("pages.profiles.distribution.columns.revision")}
        </Tooltip>
      ),
      render: (_: unknown, row: DistributionRow) =>
        row.delivery ? (
          <Typography.Text code>
            {row.delivery.applied_revision}/{row.delivery.dispatched_revision}/
            {row.delivery.desired_revision}
          </Typography.Text>
        ) : (
          "-"
        ),
    },
    {
      title: t("pages.profiles.distribution.columns.updated"),
      render: (_: unknown, row: DistributionRow) => {
        const value = row.delivery?.updated_at ?? row.createdAt;
        return value ? formatTime(value, i18n.language) : "-";
      },
    },
    {
      title: t("pages.profiles.distribution.columns.actions"),
      render: (_: unknown, row: DistributionRow) =>
        row.explicit ? (
          <Popconfirm
            title={t("pages.profiles.distribution.withdrawConfirm")}
            okText={t("common.confirm")}
            cancelText={t("common.cancel")}
            okButtonProps={{ danger: true }}
            onConfirm={() => withdrawMutation.mutate(row.gatewayId)}
          >
            <Button type="link" size="small" danger>
              {t("pages.profiles.distribution.withdraw")}
            </Button>
          </Popconfirm>
        ) : (
          "-"
        ),
    },
  ];

  return (
    <Card
      title={t("pages.profiles.distribution.title")}
      style={{ marginBottom: 16 }}
      extra={
        <Button
          type="primary"
          icon={<PlusOutlined />}
          disabled={gatewayOptions.length === 0}
          onClick={() => {
            setSelectedGateway(undefined);
            setModalOpen(true);
          }}
        >
          {t("pages.profiles.distribution.distribute")}
        </Button>
      }
    >
      <Typography.Paragraph type="secondary">
        {t("pages.profiles.distribution.hint")}
      </Typography.Paragraph>
      <Table
        rowKey="gatewayId"
        loading={targetsQuery.isLoading || deliveriesQuery.isLoading}
        columns={columns}
        dataSource={rows}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("common.totalItems", { total }),
        }}
        locale={{ emptyText: t("pages.profiles.distribution.empty") }}
      />
      <Modal
        title={t("pages.profiles.distribution.modalTitle")}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        confirmLoading={distributeMutation.isPending}
        okText={t("common.confirm")}
        cancelText={t("common.cancel")}
        okButtonProps={{ disabled: !selectedGateway }}
        onOk={() => selectedGateway && distributeMutation.mutate(selectedGateway)}
      >
        <Typography.Paragraph type="secondary">
          {t("pages.profiles.distribution.modalHint")}
        </Typography.Paragraph>
        <Select
          style={{ width: "100%" }}
          showSearch
          optionFilterProp="label"
          placeholder={t("pages.profiles.distribution.gatewayPlaceholder")}
          value={selectedGateway}
          options={gatewayOptions}
          onChange={setSelectedGateway}
        />
      </Modal>
    </Card>
  );
}
