// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// TemplateGallery presents imported, immutable template versions as cards.
// Instantiation starts a draft from the selected version; editing always
// publishes a new version, packs can be imported from or exported to YAML/JSON
// files, and deletion is available only while no instance references a version.

import { useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Col,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Row,
  Space,
  Typography,
  Upload,
} from "antd";
import {
  DeleteOutlined,
  DownloadOutlined,
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
  UploadOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import {
  deleteBusinessTemplate,
  exportBusinessTemplates,
  importBusinessTemplates,
  type Template,
} from "../../api/business";
import { extractError } from "../../api/client";
import { downloadText } from "../../utils/download";
import TemplateDesigner from "./TemplateDesigner";

interface TemplateGalleryProps {
  templates: Template[];
  loading: boolean;
  error?: unknown;
  onRefresh: () => void;
  onInstantiate: (template: Template, name: string) => void;
  onCreated: (template: Template) => void;
  onDeleted: (template: Template) => void;
}

// TemplateGallery renders version cards and the instance-name drawer.
export default function TemplateGallery({
  templates,
  loading,
  error,
  onRefresh,
  onInstantiate,
  onCreated,
  onDeleted,
}: TemplateGalleryProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const [selected, setSelected] = useState<Template>();
  const [designer, setDesigner] = useState<{ source?: Template }>();
  const [deletingKey, setDeletingKey] = useState<string>();
  const [form] = Form.useForm<{ name: string }>();

  const remove = async (template: Template) => {
    const key = `${template.id}@${template.version}`;
    setDeletingKey(key);
    try {
      await deleteBusinessTemplate(template);
      onDeleted(template);
      message.success(t("business.templates.deleted"));
    } catch (error) {
      message.error(extractError(error).message);
    } finally {
      setDeletingKey(undefined);
    }
  };

  const [importing, setImporting] = useState(false);
  const [exporting, setExporting] = useState(false);

  const importPack = async (file: File) => {
    setImporting(true);
    try {
      const imported = await importBusinessTemplates(file);
      message.success(t("business.templates.imported", { count: imported.length }));
      onRefresh();
    } catch (error) {
      message.error(extractError(error).message);
    } finally {
      setImporting(false);
    }
    return false;
  };

  const exportPack = async () => {
    setExporting(true);
    try {
      const blob = await exportBusinessTemplates();
      downloadText(
        "business-templates.yaml",
        await blob.text(),
        "application/yaml;charset=utf-8",
      );
    } catch (error) {
      message.error(extractError(error).message);
    } finally {
      setExporting(false);
    }
  };

  return (
    <>
      <Space style={{ width: "100%", justifyContent: "flex-end", marginBottom: 16 }}>
        <Button
          type="primary"
          icon={<PlusOutlined />}
          onClick={() => setDesigner({})}
        >
          {t("business.templates.create")}
        </Button>
        <Upload
          accept=".yaml,.yml,.json"
          showUploadList={false}
          beforeUpload={(file) => void importPack(file)}
        >
          <Button icon={<UploadOutlined />} loading={importing}>
            {t("business.templates.importPack")}
          </Button>
        </Upload>
        <Button
          icon={<DownloadOutlined />}
          loading={exporting}
          disabled={templates.length === 0}
          onClick={() => void exportPack()}
        >
          {t("business.templates.exportPack")}
        </Button>
        <Button icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
          {t("business.actions.refresh")}
        </Button>
      </Space>
      {error ? (
        <Alert
          type="error"
          showIcon
          message={t("business.templates.loadFailed")}
          description={extractError(error).message}
        />
      ) : templates.length === 0 && !loading ? (
        <Empty description={t("business.templates.empty")} />
      ) : (
        <Row gutter={[16, 16]}>
          {templates.map((template) => (
            <Col xs={24} sm={12} xl={8} key={`${template.id}@${template.version}`}>
              <Card
                loading={loading}
                title={template.name}
                actions={[
                  <Button
                    key="instantiate"
                    type="link"
                    icon={<PlusOutlined />}
                    disabled={deletingKey !== undefined}
                    onClick={() => setSelected(template)}
                  >
                    {t("business.templates.instantiate")}
                  </Button>,
                  <Button
                    key="edit"
                    type="link"
                    icon={<EditOutlined />}
                    disabled={deletingKey !== undefined}
                    onClick={() => setDesigner({ source: template })}
                  >
                    {t("business.templates.edit")}
                  </Button>,
                  <Popconfirm
                    key="delete"
                    title={t("business.templates.deleteTitle")}
                    description={t("business.templates.deleteDescription", {
                      id: template.id,
                      version: template.version,
                    })}
                    okText={t("business.templates.delete")}
                    cancelText={t("business.actions.cancel")}
                    okButtonProps={{ danger: true }}
                    disabled={deletingKey !== undefined}
                    onConfirm={() => void remove(template)}
                  >
                    <Button
                      danger
                      type="link"
                      icon={<DeleteOutlined />}
                      loading={deletingKey === `${template.id}@${template.version}`}
                    >
                      {t("business.templates.delete")}
                    </Button>
                  </Popconfirm>,
                ]}
              >
                <Typography.Paragraph type="secondary">
                  {template.description}
                </Typography.Paragraph>
                <Space direction="vertical" size={4}>
                  <Typography.Text>
                    {t("business.templates.templateId")}: <Typography.Text code>{template.id}</Typography.Text>
                  </Typography.Text>
                  <Typography.Text>
                    {t("business.templates.version")}: {template.version}
                  </Typography.Text>
                  <Typography.Text type="secondary">
                    {t("business.templates.schemaVersion")}: {template.schema_version}
                  </Typography.Text>
                  <Typography.Text type="secondary">
                    {t("business.templates.assetCount", {
                      count: template.assembly.nodes.length,
                    })}
                  </Typography.Text>
                </Space>
              </Card>
            </Col>
          ))}
        </Row>
      )}

      <Drawer
        open={Boolean(selected)}
        width={480}
        title={t("business.templates.instantiateTitle")}
        onClose={() => setSelected(undefined)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={() => setSelected(undefined)}>
              {t("business.actions.cancel")}
            </Button>
            <Button
              type="primary"
              onClick={() => {
                if (!selected) return;
                void form.validateFields().then(({ name }) => {
                  onInstantiate(selected, name);
                  setSelected(undefined);
                });
              }}
            >
              {t("business.templates.instantiate")}
            </Button>
          </Space>
        }
      >
        {selected && (
          <>
            <Typography.Paragraph>
              {t("business.templates.instantiateDescription", {
                name: selected.name,
                version: selected.version,
              })}
            </Typography.Paragraph>
            <Form
              form={form}
              layout="vertical"
              initialValues={{
                name: t("business.templates.defaultInstanceName", {
                  name: selected.name,
                }),
              }}
            >
              <Form.Item
                name="name"
                label={t("business.templates.instanceName")}
                rules={[
                  {
                    required: true,
                    whitespace: true,
                    message: t("business.templates.instanceNameRequired"),
                  },
                ]}
              >
                <Input autoFocus maxLength={255} />
              </Form.Item>
            </Form>
          </>
        )}
      </Drawer>
      {designer && (
        <TemplateDesigner
          source={designer.source}
          onClose={() => setDesigner(undefined)}
          onCreated={onCreated}
        />
      )}
    </>
  );
}
