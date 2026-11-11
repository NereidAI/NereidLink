// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DashboardsPage is the user-defined dashboard list (ADR-0020). It
// presents every tenant dashboard as a card; clicking a card opens
// the standalone dashboard route. New dashboards are created through
// a drawer that collects a custom name. There is no tab strip; each
// dashboard lives on its own page.

import { useMemo, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Col,
  Drawer,
  Form,
  Input,
  Row,
  Space,
} from "antd";
import {
  AppstoreOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate } from "react-router-dom";
import dayjs from "dayjs";

import PageHeader from "../components/PageHeader";
import FilterSearchBar from "../components/FilterSearchBar";
import {
  createDashboard,
  listDashboards,
} from "../api/dashboards";
import { extractError } from "../api/client";

// CreateFormValues is the create drawer shape.
interface CreateFormValues {
  name: string;
}

// DashboardsPage renders the dashboard cards and create drawer.
export default function DashboardsPage() {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const [createOpen, setCreateOpen] = useState(false);
  const [searchInput, setSearchInput] = useState("");
  const isZh = i18n.language.startsWith("zh");

  const dashboardsQuery = useQuery({
    queryKey: ["dashboards"],
    queryFn: listDashboards,
  });
  // Memoized so the filtered-list useMemo keeps a stable dependency
  // identity instead of seeing a fresh [] fallback on every render.
  const dashboards = useMemo(() => dashboardsQuery.data ?? [], [dashboardsQuery.data]);

  // visibleDashboards applies the client-side name filter; the list
  // endpoint is unpaged, so filtering happens locally.
  const visibleDashboards = useMemo(() => {
    const needle = searchInput.trim().toLowerCase();
    if (!needle) return dashboards;
    return dashboards.filter((dash) =>
      dash.name.toLowerCase().includes(needle),
    );
  }, [dashboards, searchInput]);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["dashboards"] });

  const createMutation = useMutation({
    mutationFn: (name: string) =>
      createDashboard({ name, layout: [] }),
    onSuccess: async (dash) => {
      await invalidate();
      setCreateOpen(false);
      message.success(t("pages.dashboards.toasts.created"));
      navigate(`/dashboards/${dash.id}`);
    },
    onError: (error) => message.error(extractError(error).message),
  });

  // formatTime renders the card update timestamp.
  const formatTime = (value: string) =>
    dayjs(value).format(isZh
      ? "YYYY/MM/DD HH:mm"
      : "M/D/YYYY, h:mm A");

  return (
    <>
      <PageHeader
        title={t("pages.dashboards.title")}
        subtitle={t("pages.dashboards.subtitle")}
        helpId="dashboards"
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => setCreateOpen(true)}
          >
            {t("pages.dashboards.create")}
          </Button>
        }
      />

      <FilterSearchBar
        searchValue={searchInput}
        onSearchChange={setSearchInput}
        searchPlaceholder={t("pages.dashboards.searchPlaceholder")}
      />

      <Row gutter={[16, 16]}>
        {visibleDashboards.map((dash) => (
          <Col key={dash.id} xs={24} sm={12} md={8} lg={6}>
            <Card
              hoverable
              onClick={() => navigate(`/dashboards/${dash.id}`)}
            >
              <Card.Meta
                avatar={<AppstoreOutlined style={{ fontSize: 28, color: "#1677ff" }} />}
                title={dash.name}
                description={
                  <div>
                    <div>{t("pages.dashboards.widgetCount", { count: dash.layout.length })}</div>
                    <div>{formatTime(dash.updated_at)}</div>
                  </div>
                }
              />
            </Card>
          </Col>
        ))}
      </Row>

      {visibleDashboards.length === 0 && !dashboardsQuery.isLoading && (
        <div style={{ padding: 48, textAlign: "center", color: "#999" }}>
          {searchInput.trim()
            ? t("pages.dashboards.noSearchResults")
            : t("pages.dashboards.empty")}
        </div>
      )}

      <CreateDashboardModal
        open={createOpen}
        existingNames={dashboards.map((dash) => dash.name)}
        creating={createMutation.isPending}
        onCancel={() => setCreateOpen(false)}
        onSubmit={(name) => createMutation.mutate(name)}
      />
    </>
  );
}

// CreateDashboardModal collects a custom, unique dashboard name.
function CreateDashboardModal({
  open,
  existingNames,
  creating,
  onCancel,
  onSubmit,
}: {
  open: boolean;
  existingNames: string[];
  creating: boolean;
  onCancel: () => void;
  onSubmit: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<CreateFormValues>();

  return (
    <Drawer
      open={open}
      title={t("pages.dashboards.createTitle")}
      width={720}
      onClose={onCancel}
      destroyOnHidden
      footer={null}
    >
      <Form<CreateFormValues>
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => onSubmit(values.name.trim())}
      >
        <Form.Item
          label={t("pages.dashboards.nameLabel")}
          name="name"
          rules={[
            {
              required: true,
              message: t("pages.dashboards.nameRequired"),
            },
            {
              whitespace: true,
              message: t("pages.dashboards.nameRequired"),
            },
            () => ({
              validator(_, value: string) {
                if (value && existingNames.includes(value.trim())) {
                  return Promise.reject(new Error(t("pages.dashboards.nameExists")));
                }
                return Promise.resolve();
              },
            }),
          ]}
        >
          <Input
            autoFocus
            maxLength={200}
            placeholder={t("pages.dashboards.namePlaceholder")}
          />
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onCancel}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={creating}>
              {t("common.save")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}
