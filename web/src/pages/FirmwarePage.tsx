// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// FirmwarePage manages the platform-hosted firmware repository: streamed
// upload, profile/model filter, stable marking, deletion and operator
// presigned download. Listing is server-paged through useServerTable.

import { useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  CloudDownloadOutlined,
  DeleteOutlined,
  PlusOutlined,
  StarFilled,
  StarOutlined,
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import FirmwareUploadDrawer from "../components/FirmwareUploadDrawer";
import { useDebounce } from "../hooks/useDebounce";
import { useServerTable } from "../hooks/useServerTable";
import {
  deleteFirmware,
  listFirmware,
  presignFirmware,
  setFirmwareStable,
  type FirmwarePackage,
} from "../api/firmware";
import { listProfiles } from "../api/devices";
import { extractError } from "../api/client";
import { formatBytes } from "../utils/format";
import { formatDateTime } from "../utils/format";

// FirmwarePage renders the repository list and upload drawer.
export default function FirmwarePage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [uploadOpen, setUploadOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [profileFilter, setProfileFilter] = useState("");
  const debouncedSearch = useDebounce(search, 300);

  const { items, loading, pagination, onChange } = useServerTable(
    "firmware",
    { search: debouncedSearch.trim(), profile_id: profileFilter },
    (params) =>
      listFirmware({
        search: params.search || undefined,
        profile_id: params.profile_id || undefined,
        page: params.page,
        page_size: params.page_size,
      }),
  );

  const profilesQuery = useQuery({
    queryKey: ["device-profiles"],
    queryFn: listProfiles,
  });
  const profileName = (id?: string) =>
    id ? (profilesQuery.data?.find((p) => p.id === id)?.name ?? id) : "";

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["firmware"] });

  const stableMutation = useMutation({
    mutationFn: ({ id, stable }: { id: string; stable: boolean }) =>
      setFirmwareStable(id, stable),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.firmware.toasts.stableSet"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: deleteFirmware,
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.firmware.toasts.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const downloadMutation = useMutation({
    mutationFn: presignFirmware,
    onSuccess: (result) => {
      window.open(result.url, "_blank", "noopener,noreferrer");
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns = [
    {
      title: t("pages.firmware.release"),
      dataIndex: "name",
      render: (_: unknown, record: FirmwarePackage) => (
        <Space size={6}>
          <Tooltip
            title={
              record.release_notes ? (
                <span style={{ whiteSpace: "pre-wrap" }}>
                  {record.release_notes}
                </span>
              ) : undefined
            }
          >
            <Typography.Text strong>{record.name}</Typography.Text>
          </Tooltip>
          <Typography.Text code style={{ fontSize: 11 }}>
            {record.version}
          </Typography.Text>
          {record.is_stable && (
            <Tag color="gold" icon={<StarFilled />}>
              {t("pages.firmware.stable")}
            </Tag>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.firmware.profile"),
      dataIndex: "profile_id",
      render: (id?: string) =>
        id ? (
          <Tag>{profileName(id)}</Tag>
        ) : (
          <Typography.Text type="secondary">
            {t("pages.firmware.profileGeneric")}
          </Typography.Text>
        ),
    },
    {
      title: t("pages.firmware.filename"),
      dataIndex: "filename",
      ellipsis: true,
    },
    {
      title: t("pages.firmware.size"),
      dataIndex: "size_bytes",
      width: 110,
      render: (size: number) => formatBytes(size),
    },
    {
      title: t("pages.firmware.sha256"),
      dataIndex: "sha256",
      width: 150,
      ellipsis: true,
      render: (sha: string) => (
        <Typography.Text code style={{ fontSize: 11 }}>
          {sha.slice(0, 16)}…
        </Typography.Text>
      ),
    },
    {
      title: t("pages.firmware.createdAt"),
      dataIndex: "created_at",
      width: 170,
      render: (value: string) => formatDateTime(value),
    },
    {
      title: t("pages.firmware.actions"),
      key: "actions",
      width: 130,
      render: (_: unknown, record: FirmwarePackage) => (
        <Space>
          <Tooltip
            title={
              record.is_stable
                ? t("pages.firmware.unmarkStable")
                : t("pages.firmware.markStable")
            }
          >
            <Button
              type="text"
              size="small"
              icon={record.is_stable ? <StarFilled /> : <StarOutlined />}
              onClick={() =>
                stableMutation.mutate({ id: record.id, stable: !record.is_stable })
              }
            />
          </Tooltip>
          <Tooltip title={t("pages.firmware.download")}>
            <Button
              type="text"
              size="small"
              icon={<CloudDownloadOutlined />}
              loading={downloadMutation.isPending}
              onClick={() => downloadMutation.mutate(record.id)}
            />
          </Tooltip>
          <Popconfirm
            title={t("pages.firmware.confirm.title")}
            okText={t("common.delete")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMutation.mutate(record.id)}
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
        title={t("pages.firmware.title")}
        subtitle={t("pages.firmware.subtitle")}
        helpId="firmware"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setUploadOpen(true)}
          >
            {t("pages.firmware.upload")}
          </Button>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={search}
          onSearchChange={setSearch}
          searchPlaceholder={t("pages.firmware.filters.searchPlaceholder")}
          filters={[
            {
              key: "profile_id",
              placeholder: t("pages.firmware.filters.profileAll"),
              value: profileFilter,
              onChange: setProfileFilter,
              options: (profilesQuery.data ?? []).map((profile) => ({
                value: profile.id,
                label: profile.name,
              })),
            },
          ]}
        />
        <Table<FirmwarePackage>
          rowKey="id"
          loading={loading}
          columns={columns}
          dataSource={items}
          pagination={pagination}
          onChange={onChange}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <FirmwareUploadDrawer open={uploadOpen} onClose={() => setUploadOpen(false)} />
    </>
  );
}
