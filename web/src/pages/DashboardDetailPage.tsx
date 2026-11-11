// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DashboardDetailPage renders one dashboard on its own route
// (ADR-0020). It loads the dashboard list to resolve the path id,
// renders the grid canvas, and supports edit/save. Deletion returns
// to the dashboard list. The canvas remounts per dashboard id so its
// widget state initialises from the loaded layout.

import { useState } from "react";
import {
  App as AntApp,
  Button,
  Dropdown,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Space,
} from "antd";
import {
  ArrowLeftOutlined,
  EditOutlined,
  PlusOutlined,
  SettingOutlined,
} from "@ant-design/icons";
import {
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { useNavigate, useParams } from "react-router-dom";
import RGL, { WidthProvider } from "react-grid-layout";

import "react-grid-layout/css/styles.css";
import "react-resizable/css/styles.css";

import PageHeader from "../components/PageHeader";
import WidgetRenderer from "../components/dashboard/WidgetRenderer";
import WidgetConfigDrawer from "../components/dashboard/WidgetConfigDrawer";
import {
  deleteDashboard,
  listDashboards,
  updateDashboard,
  type Dashboard,
  type Widget,
} from "../api/dashboards";
import { extractError } from "../api/client";

// GridLayout with automatic width from the parent container.
const GridLayout = WidthProvider(RGL);

// GRID_COLS / ROW_HEIGHT define the grid geometry.
const GRID_COLS = 24;
const ROW_HEIGHT = 48;

// newWidgetId generates a locally unique widget key.
let widgetSeq = 0;
function newWidgetId(): string {
  widgetSeq += 1;
  return `w-${Date.now()}-${widgetSeq}`;
}

// DashboardDetailPage renders the standalone dashboard canvas.
export default function DashboardDetailPage() {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const { dashboardId } = useParams();
  const [editing, setEditing] = useState(false);
  const [renameOpen, setRenameOpen] = useState(false);

  const dashboardsQuery = useQuery({
    queryKey: ["dashboards"],
    queryFn: listDashboards,
  });
  const dashboards = dashboardsQuery.data ?? [];
  const dashboard = dashboards.find((dash) => dash.id === dashboardId);

  const invalidate = () =>
    queryClient.invalidateQueries({ queryKey: ["dashboards"] });

  const onError = (error: unknown) =>
    message.error(extractError(error).message);

  const saveMutation = useMutation({
    mutationFn: (args: { dash: Dashboard; layout: Widget[] }) =>
      updateDashboard(args.dash.id, {
        name: args.dash.name,
        sort_order: args.dash.sort_order,
        layout: args.layout,
      }),
    onSuccess: async () => {
      await invalidate();
      setEditing(false);
      message.success(t("pages.dashboards.saved"));
    },
    onError,
  });

  const renameMutation = useMutation({
    mutationFn: (name: string) => {
      if (!dashboard) {
        throw new Error("dashboard not found");
      }
      return updateDashboard(dashboard.id, {
        name,
        sort_order: dashboard.sort_order,
        layout: dashboard.layout,
      });
    },
    onSuccess: async () => {
      await invalidate();
      setRenameOpen(false);
      message.success(t("pages.dashboards.toasts.renamed"));
    },
    onError,
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteDashboard(id),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.dashboards.toasts.deleted"));
      navigate("/dashboards");
    },
    onError,
  });

  if (!dashboard) {
    return (
      <>
        <BackButton />
        <div style={{ padding: 48, textAlign: "center", color: "#999" }}>
          {t("pages.dashboards.notFound")}
        </div>
      </>
    );
  }

  return (
    <>
      <PageHeader
        title={dashboard.name}
        subtitle={t("pages.dashboards.subtitle")}
        helpId="dashboards"
        extra={
          <Space>
            <Button
              icon={<ArrowLeftOutlined />}
              onClick={() => navigate("/dashboards")}
            >
              {t("pages.dashboards.back")}
            </Button>
            {!editing && (
              <Button
                icon={<EditOutlined />}
                onClick={() => setRenameOpen(true)}
              >
                {t("pages.dashboards.rename")}
              </Button>
            )}
            {!editing && (
              <Button
                icon={<SettingOutlined />}
                onClick={() => setEditing(true)}
              >
                {t("pages.dashboards.edit")}
              </Button>
            )}
            <Popconfirm
              title={t("pages.dashboards.confirmDelete")}
              onConfirm={() => deleteMutation.mutate(dashboard.id)}
            >
              <Button danger>{t("common.delete")}</Button>
            </Popconfirm>
          </Space>
        }
      />

      <DashboardCanvas
        key={dashboard.id}
        dashboard={dashboard}
        editing={editing}
        onCancelEdit={() => setEditing(false)}
        onSave={(layout) =>
          saveMutation.mutate({ dash: dashboard, layout })
        }
        saving={saveMutation.isPending}
      />

      <RenameModal
        open={renameOpen}
        currentName={dashboard.name}
        existingNames={dashboards
          .filter((dash) => dash.id !== dashboard.id)
          .map((dash) => dash.name)}
        saving={renameMutation.isPending}
        onCancel={() => setRenameOpen(false)}
        onSubmit={(name) => renameMutation.mutate(name)}
      />
    </>
  );
}

// RenameFormValues is the rename drawer shape.
interface RenameFormValues {
  name: string;
}

// RenameModal collects a new unique dashboard name inside a drawer.
function RenameModal({
  open,
  currentName,
  existingNames,
  saving,
  onCancel,
  onSubmit,
}: {
  open: boolean;
  currentName: string;
  existingNames: string[];
  saving: boolean;
  onCancel: () => void;
  onSubmit: (name: string) => void;
}) {
  const { t } = useTranslation();
  const [form] = Form.useForm<RenameFormValues>();

  return (
    <Drawer
      open={open}
      title={t("pages.dashboards.renameTitle")}
      width={720}
      onClose={onCancel}
      destroyOnHidden
      footer={null}
      afterOpenChange={(visible) => {
        if (visible) {
          form.setFieldsValue({ name: currentName });
        }
      }}
    >
      <Form<RenameFormValues>
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => onSubmit(values.name.trim())}
      >
        <Form.Item
          label={t("pages.dashboards.nameLabel")}
          name="name"
          rules={[
            { required: true, whitespace: true, message: t("pages.dashboards.nameRequired") },
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
          <Input autoFocus maxLength={200} />
        </Form.Item>
        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onCancel}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={saving}>
              {t("common.save")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// BackButton renders a link back to the dashboard list.
function BackButton() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  return (
    <Button
      type="link"
      icon={<ArrowLeftOutlined />}
      style={{ paddingLeft: 0 }}
      onClick={() => navigate("/dashboards")}
    >
      {t("pages.dashboards.back")}
    </Button>
  );
}

// DashboardCanvas renders one dashboard grid and owns the working widget
// layout while editing. It mounts fresh per dashboard id.
function DashboardCanvas({
  dashboard,
  editing,
  saving,
  onSave,
  onCancelEdit,
}: {
  dashboard: Dashboard;
  editing: boolean;
  saving: boolean;
  onSave: (layout: Widget[]) => void;
  onCancelEdit: () => void;
}) {
  const { t } = useTranslation();
  const [widgets, setWidgets] = useState<Widget[]>(dashboard.layout);
  const [configuring, setConfiguring] = useState<Widget | undefined>();

  // addWidget appends a new unbound widget of the chosen type.
  const addWidget = (type: Widget["type"]) => {
    setWidgets((current) => [
      ...current,
      {
        id: newWidgetId(),
        type,
        x: 0,
        y: 99,
        w: type === "stat" ? 6 : 8,
        h: type === "stat" ? 4 : 6,
        range: "1h",
      },
    ]);
  };

  // onLayoutChange maps the new geometry back onto the working widgets.
  const onLayoutChange = (layout: RGL.Layout[]) => {
    setWidgets((current) =>
      current.map((widget) => {
        const item = layout.find((entry) => entry.i === widget.id);
        return item
          ? {
              ...widget,
              x: item.x,
              y: item.y,
              w: item.w,
              h: item.h,
            }
          : widget;
      }),
    );
  };

  const removeWidget = (id: string) =>
    setWidgets((current) => current.filter((widget) => widget.id !== id));

  return (
    <div style={{ position: "relative", minHeight: "60vh" }}>
      {editing && (
        <Space style={{ marginBottom: 8 }}>
          <Button type="primary" loading={saving} onClick={() => onSave(widgets)}>
            {t("common.save")}
          </Button>
          <Button onClick={onCancelEdit}>{t("common.cancel")}</Button>
          <Dropdown
            menu={{
              items: [
                { key: "timeseries", label: t("pages.dashboards.add.timeseries") },
                { key: "stat", label: t("pages.dashboards.add.stat") },
                { key: "alarms", label: t("pages.dashboards.add.alarms") },
              ],
              onClick: ({ key }) => addWidget(key as Widget["type"]),
            }}
          >
            <Button icon={<PlusOutlined />}>
              {t("pages.dashboards.addWidget")}
            </Button>
          </Dropdown>
        </Space>
      )}

      <GridLayout
        className="layout"
        layout={widgets.map((widget) => ({
          i: widget.id,
          x: widget.x,
          y: widget.y,
          w: widget.w,
          h: widget.h,
        }))}
        cols={GRID_COLS}
        rowHeight={ROW_HEIGHT}
        isDraggable={editing}
        isResizable={editing}
        draggableHandle=".widget-drag-handle"
        margin={[12, 12]}
        onLayoutChange={onLayoutChange}
      >
        {widgets.map((widget) => (
          <div
            key={widget.id}
            style={{
              background: "#fff",
              border: "1px solid #f0f0f0",
              borderRadius: 6,
            }}
          >
            {editing && (
              <div style={{ position: "absolute", top: 2, right: 6, zIndex: 2 }}>
                <Space size={2}>
                  <Button
                    type="text"
                    size="small"
                    onClick={() => setConfiguring(widget)}
                  >
                    <SettingOutlined />
                  </Button>
                  <Button
                    type="text"
                    size="small"
                    danger
                    onClick={() => removeWidget(widget.id)}
                  >
                    x
                  </Button>
                </Space>
              </div>
            )}
            {editing ? (
              <div className="widget-drag-handle" style={{ height: "100%" }}>
                <WidgetRenderer widget={widget} />
              </div>
            ) : (
              <WidgetRenderer widget={widget} />
            )}
          </div>
        ))}
      </GridLayout>

      <WidgetConfigDrawer
        widget={configuring}
        onCancel={() => setConfiguring(undefined)}
        onSubmit={(updated) => {
          setWidgets((current) =>
            current.map((widget) =>
              widget.id === updated.id ? updated : widget,
            ),
          );
          setConfiguring(undefined);
        }}
      />
    </div>
  );
}
