// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfilesPage is the device profile list: PageHeader, Card, table of
// profiles with a link into the independent detail page, and lightweight
// create/import drawers. Editing a profile lives on /profiles/:profileId so
// the connector parameters and point library are top-level sections, not a
// tab nested inside a drawer.

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
import { CodeOutlined, DeleteOutlined, PlusOutlined, UploadOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router-dom";
import { useMemo, useState } from "react";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import CreateProfileDrawer from "../components/CreateProfileDrawer";
import BulkImportDrawer from "../components/BulkImportDrawer";
import ProfileDocumentImportDrawer from "../components/ProfileDocumentImportDrawer";
import {
  bulkImportProfiles,
  deleteProfile,
  listProfiles,
  previewProfileImport,
  profileImportTemplateUrl,
  type Profile,
} from "../api/profiles";

// ProfilesPage renders the profile management page.
export default function ProfilesPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const [importOpen, setImportOpen] = useState(false);
  const [docImportOpen, setDocImportOpen] = useState(false);
  const [keyword, setKeyword] = useState("");

  // staleTime 0: profiles are a small set and operators expect edits made
  // elsewhere to show immediately whenever the page is entered.
  const query = useQuery({
    queryKey: ["profiles"],
    queryFn: listProfiles,
    staleTime: 0,
  });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const profiles = useMemo(() => query.data ?? [], [query.data]);

  // filteredProfiles applies client-side keyword search over the profile
  // name; the list is unpaged and small.
  const filteredProfiles = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    if (!needle) return profiles;
    return profiles.filter((profile) =>
      profile.name.toLowerCase().includes(needle),
    );
  }, [profiles, keyword]);

  // Editing needs full descriptors; the list already embeds them through
  // the registry API and the Profile type in profiles.ts shares the shape.
  const deleteMutation = useMutation({
    mutationFn: deleteProfile,
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["profiles"] });
      message.success(t("pages.profiles.toasts.deleted"));
    },
    onError: () => message.error(t("pages.profiles.toasts.deleteFailed")),
  });

  const columns = [
    {
      title: t("common.name"),
      dataIndex: "name",
      render: (name: string, record: Profile) => (
        <Link to={`/profiles/${record.id}`}>
          <Typography.Text strong>{name}</Typography.Text>
        </Link>
      ),
    },
    {
      title: t("pages.profiles.idTitle"),
      dataIndex: "id",
      width: 320,
      render: (id: string) => (
        <Tooltip title={id}>
          <Typography.Text
            code
            copyable={{ text: id, tooltips: [t("common.copy"), t("common.copied")] }}
            style={{ fontSize: 12 }}
          >
            {id}
          </Typography.Text>
        </Tooltip>
      ),
    },
    {
      title: t("pages.profiles.descriptorsTitle"),
      key: "descriptor_count",
      width: 140,
      render: (_: unknown, record: Profile) =>
        t("pages.profiles.descriptorsCount", {
          count: record.descriptors?.length ?? 0,
        }),
    },
    {
      title: t("pages.connectors.columns.type"),
      key: "connector_type",
      width: 160,
      render: (_: unknown, record: Profile) =>
        record.connector_type ? (
          <Tag color="geekblue">
            {record.connector_type}
            {record.connector_type_version ? `@${record.connector_type_version}` : ""}
          </Tag>
        ) : (
          <Tooltip title={t("pages.profiles.noConnectorTemplate")}>
            <Typography.Text type="secondary">—</Typography.Text>
          </Tooltip>
        ),
    },
    {
      title: t("pages.profiles.sourceTitle"),
      key: "source",
      width: 150,
      render: (_: unknown, record: Profile) => {
        const source = record.source ?? "manual";
        const discovered = record.availability === "discovered";
        const color = source === "edge" ? "green" : source === "import" ? "gold" : "default";
        return (
          <Space size={4}>
            <Tag color={color}>{t(`pages.profiles.sources.${source}`)}</Tag>
            {discovered && (
              <Tooltip title={t("pages.profiles.discoveredHint")}>
                <Tag>{t("pages.profiles.discovered")}</Tag>
              </Tooltip>
            )}
            {record.protocol && <Tag color="purple">{record.protocol}</Tag>}
          </Space>
        );
      },
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 120,
      render: (_: unknown, record: Profile) => (
        <Space>
          <Link to={`/profiles/${record.id}`}>
            <Button type="text" size="small">
              {t("common.edit")}
            </Button>
          </Link>
          <Popconfirm
            title={t("pages.profiles.confirm.title")}
            description={t("pages.profiles.confirm.desc")}
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
        title={t("pages.profiles.title")}
        subtitle={t("pages.profiles.subtitle")}
        helpId="profiles"
        extra={
          <Space>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => setCreateOpen(true)}
            >
              {t("pages.profiles.create")}
            </Button>
            <Button icon={<UploadOutlined />} onClick={() => setImportOpen(true)}>
              {t("pages.profiles.importCsv")}
            </Button>
            <Button icon={<CodeOutlined />} onClick={() => setDocImportOpen(true)}>
              {t("pages.profiles.docImport.button")}
            </Button>
          </Space>
        }
      />
      <Card>
        <FilterSearchBar
          searchValue={keyword}
          onSearchChange={setKeyword}
          searchPlaceholder={t("pages.profiles.filters.searchPlaceholder")}
        />
        <Table
          rowKey="id"
          loading={query.isFetching}
          columns={columns}
          dataSource={filteredProfiles as Profile[]}
          pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
          locale={{ emptyText: t("common.noFilterResults") }}
        />
      </Card>
      <CreateProfileDrawer
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(id) => navigate(`/profiles/${id}`)}
      />
      <BulkImportDrawer
        open={importOpen}
        title={t("pages.profiles.importTitle")}
        templateUrl={profileImportTemplateUrl()}
        accept=".csv"
        formatHint="CSV"
        preview={previewProfileImport}
        doImport={bulkImportProfiles}
        onImported={() => queryClient.invalidateQueries({ queryKey: ["profiles"] })}
        onClose={() => setImportOpen(false)}
      />
      <ProfileDocumentImportDrawer
        open={docImportOpen}
        onClose={() => setDocImportOpen(false)}
        onImported={() => queryClient.invalidateQueries({ queryKey: ["profiles"] })}
      />
    </>
  );
}
