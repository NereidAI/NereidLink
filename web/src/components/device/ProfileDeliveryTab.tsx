// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileDeliveryTab renders, on a gateway's detail page, the device-profile
// downlink surface (ADR-0038): every library profile the gateway should hold
// with its desired/dispatched/applied revisions and the edge-reported state.
// Management mirrors the secret-delivery tab: pick a library profile to
// distribute to THIS gateway, watch apply state, and withdraw explicit edges.
// Implicit (connector-reference) rows are read-only because there is no
// explicit target edge to remove. Unlike connectors/secrets there is (yet) no
// user-plane WS channel, so the table polls only while a row is in flight and
// exposes a manual refresh otherwise.

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Modal,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import dayjs from "dayjs";

import {
  listProfileDeliveriesForGateway,
  distributeProfile,
  withdrawProfileTarget,
  type ProfileDelivery,
} from "../../api/profileDelivery";
import { listProfiles } from "../../api/profiles";
import { extractError } from "../../api/client";
import { pendingProfileDeliveryStates } from "../../utils/profileDeliveryState";
import ProfileDeliveryStateTag from "../profile/ProfileDeliveryStateTag";

// formatTime renders an RFC3339 timestamp in the locale's date-time format.
function formatTime(value: string, language: string): string {
  return dayjs(value).format(
    language.startsWith("zh") ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A",
  );
}

interface ProfileDeliveryTabProps {
  // gatewayId is the gateway device whose deliveries are listed/managed.
  gatewayId: string;
  // supported reflects the profiles feature bit negotiated in the last hello;
  // false means old firmware that never receives a profile frame.
  supported: boolean;
}

// ProfileDeliveryTab renders the gateway profile delivery table.
export default function ProfileDeliveryTab({
  gatewayId,
  supported,
}: ProfileDeliveryTabProps) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();

  const deliveriesKey = ["gateway-profile-deliveries", gatewayId] as const;
  const deliveriesQuery = useQuery({
    queryKey: deliveriesKey,
    queryFn: () => listProfileDeliveriesForGateway(gatewayId),
    // Poll only while at least one row has not reached a terminal state, so an
    // edge APPLIED/FAILED shows up without a manual refresh; stop polling once
    // everything settles.
    refetchInterval: (query) => {
      const rows = query.state.data;
      return rows && rows.some((row) => pendingProfileDeliveryStates.has(row.state))
        ? 10_000
        : false;
    },
  });

  // The profile library is a small, shared, cached query; it feeds the
  // distribute picker.
  const profilesQuery = useQuery({ queryKey: ["profiles"], queryFn: listProfiles });

  const deliveries = deliveriesQuery.data ?? [];
  const explicitIds = useMemo(
    () =>
      new Set(
        (deliveriesQuery.data ?? [])
          .filter((row) => row.explicit)
          .map((row) => row.profile_id),
      ),
    [deliveriesQuery.data],
  );

  // Only profiles without an explicit edge are offered; a profile the gateway
  // holds only through a connector can still be promoted to an explicit target.
  const profileOptions = useMemo(
    () =>
      (profilesQuery.data ?? [])
        .filter((profile) => !explicitIds.has(profile.id))
        .map((profile) => ({ value: profile.id, label: profile.name })),
    [profilesQuery.data, explicitIds],
  );

  // invalidateGateway invalidates this table; invalidateProfileViews keeps the
  // profile-detail distribution card consistent when it is opened later.
  const invalidateAfterMutation = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: deliveriesKey }),
      queryClient.invalidateQueries({ queryKey: ["profile-targets"] }),
      queryClient.invalidateQueries({
        queryKey: ["profile-deliveries-for-profile"],
      }),
    ]);

  const [modalOpen, setModalOpen] = useState(false);
  const [selectedProfile, setSelectedProfile] = useState<string | undefined>();

  const distributeMutation = useMutation({
    mutationFn: (profileId: string) =>
      distributeProfile(profileId, gatewayId),
    onSuccess: async () => {
      message.success(t("pages.deviceDetail.profileDelivery.successDistribute"));
      setModalOpen(false);
      setSelectedProfile(undefined);
      await invalidateAfterMutation();
    },
    onError: (error: unknown) => message.error(extractError(error).message),
  });

  const withdrawMutation = useMutation({
    mutationFn: (profileId: string) =>
      withdrawProfileTarget(profileId, gatewayId),
    onSuccess: async () => {
      message.success(t("pages.deviceDetail.profileDelivery.successWithdraw"));
      await invalidateAfterMutation();
    },
    onError: (error: unknown) => message.error(extractError(error).message),
  });

  const columns = [
    {
      title: t("pages.deviceDetail.profileDelivery.columns.profile"),
      dataIndex: "profile_name",
      render: (name: string, delivery: ProfileDelivery) => (
        <Link to={`/profiles/${delivery.profile_id}`}>
          <strong>{name}</strong>
        </Link>
      ),
    },
    {
      title: t("pages.deviceDetail.profileDelivery.columns.source"),
      render: (_: unknown, delivery: ProfileDelivery) =>
        delivery.explicit ? (
          <Tag color="blue">
            {t("pages.deviceDetail.profileDelivery.sourceExplicit")}
          </Tag>
        ) : (
          <Tooltip
            title={t(
              "pages.deviceDetail.profileDelivery.sourceImplicitTip",
            )}
          >
            <Tag>{t("pages.deviceDetail.profileDelivery.sourceImplicit")}</Tag>
          </Tooltip>
        ),
    },
    {
      title: t("pages.deviceDetail.profileDelivery.columns.status"),
      render: (_: unknown, delivery: ProfileDelivery) => (
        <ProfileDeliveryStateTag state={delivery.state} reason={delivery.reason} />
      ),
    },
    {
      title: (
        <Tooltip title={t("pages.deviceDetail.profileDelivery.revisionTip")}>
          {t("pages.deviceDetail.profileDelivery.columns.revision")}
        </Tooltip>
      ),
      render: (_: unknown, delivery: ProfileDelivery) => (
        <Typography.Text code>
          {delivery.applied_revision}/{delivery.dispatched_revision}/
          {delivery.desired_revision}
        </Typography.Text>
      ),
    },
    {
      title: t("pages.deviceDetail.profileDelivery.columns.updated"),
      dataIndex: "updated_at",
      render: (value: string) =>
        value ? formatTime(value, i18n.language) : "-",
    },
    {
      title: t("pages.deviceDetail.profileDelivery.columns.actions"),
      render: (_: unknown, delivery: ProfileDelivery) =>
        delivery.explicit ? (
          <Popconfirm
            title={t("pages.deviceDetail.profileDelivery.withdrawConfirm")}
            okText={t("common.confirm")}
            cancelText={t("common.cancel")}
            okButtonProps={{ danger: true }}
            onConfirm={() => withdrawMutation.mutate(delivery.profile_id)}
          >
            <Button type="link" size="small" danger>
              {t("pages.deviceDetail.profileDelivery.withdraw")}
            </Button>
          </Popconfirm>
        ) : (
          "-"
        ),
    },
  ];

  return (
    <>
      {!supported && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t(
            "pages.deviceDetail.profileDelivery.unsupported.title",
          )}
          description={t(
            "pages.deviceDetail.profileDelivery.unsupported.description",
          )}
        />
      )}
      <Space
        style={{ marginBottom: 12, justifyContent: "space-between", width: "100%" }}
      >
        <Typography.Text type="secondary">
          {t("pages.deviceDetail.profileDelivery.hint")}
        </Typography.Text>
        <Space>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={profileOptions.length === 0}
            onClick={() => {
              setSelectedProfile(undefined);
              setModalOpen(true);
            }}
          >
            {t("pages.deviceDetail.profileDelivery.distribute")}
          </Button>
          <Button
            size="small"
            icon={<ReloadOutlined />}
            loading={deliveriesQuery.isFetching}
            onClick={() => void deliveriesQuery.refetch()}
          >
            {t("pages.deviceDetail.profileDelivery.refresh")}
          </Button>
        </Space>
      </Space>
      <Table
        rowKey="profile_id"
        loading={deliveriesQuery.isLoading}
        columns={columns}
        dataSource={deliveries}
        pagination={{
          defaultPageSize: 10,
          showSizeChanger: true,
          showTotal: (total) => t("common.totalItems", { total }),
        }}
        locale={{ emptyText: t("pages.deviceDetail.profileDelivery.empty") }}
      />
      <Modal
        title={t("pages.deviceDetail.profileDelivery.modalTitle")}
        open={modalOpen}
        onCancel={() => setModalOpen(false)}
        confirmLoading={distributeMutation.isPending}
        okText={t("common.confirm")}
        cancelText={t("common.cancel")}
        okButtonProps={{ disabled: !selectedProfile }}
        onOk={() =>
          selectedProfile && distributeMutation.mutate(selectedProfile)
        }
      >
        <Typography.Paragraph type="secondary">
          {t("pages.deviceDetail.profileDelivery.modalHint")}
        </Typography.Paragraph>
        <Select
          style={{ width: "100%" }}
          showSearch
          optionFilterProp="label"
          placeholder={t(
            "pages.deviceDetail.profileDelivery.profilePlaceholder",
          )}
          value={selectedProfile}
          options={profileOptions}
          onChange={setSelectedProfile}
        />
      </Modal>
    </>
  );
}
