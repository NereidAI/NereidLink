// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// TemplateDesigner creates one immutable custom template version from a blank
// assembly or an independent copy of an existing version. It reuses the
// business canvas and server-side document validation; no executable content
// or per-frame pipeline editor is added to the template workflow.

import { useState } from "react";
import {
  Alert,
  App as AntApp,
  Button,
  Col,
  Drawer,
  Form,
  Input,
  Row,
  Space,
  Typography,
} from "antd";
import { SaveOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";

import {
  createBusinessTemplate,
  type Assembly,
  type Template,
} from "../../api/business";
import { extractError } from "../../api/client";
import BusinessCanvas from "./BusinessCanvas";
import { prepareAssemblyForSave } from "./businessModel";

// TemplateDesignerValues contains metadata plus the editable JSON Schema for
// scalar instance parameters. Nested assets stay in their canvas inspectors.
interface TemplateDesignerValues {
  id: string;
  version: string;
  name: string;
  description: string;
  parametersSchema: string;
}

interface TemplateDesignerProps {
  source?: Template;
  onClose: () => void;
  onCreated: (template: Template) => void;
}

// cloneJSON isolates a designer draft from the immutable TanStack Query cache.
function cloneJSON<T>(value: T): T {
  return JSON.parse(JSON.stringify(value)) as T;
}

// nextTemplateVersion suggests a fresh patch version for edit-and-publish.
// Non-semver identifiers remain valid by receiving a numeric suffix instead of
// being rejected by a client-side versioning policy the server does not impose.
function nextTemplateVersion(version: string): string {
  const match = version.match(/^(\d+)\.(\d+)\.(\d+)$/);
  if (!match) return `${version}.1`;
  return `${match[1]}.${match[2]}.${Number(match[3]) + 1}`;
}

// TemplateDesigner renders the complete create-version workflow in a wide
// drawer so the metadata and assembly remain visible in one save operation.
export default function TemplateDesigner({
  source,
  onClose,
  onCreated,
}: TemplateDesignerProps) {
  const { t } = useTranslation();
  const { message } = AntApp.useApp();
  const editing = Boolean(source);
  const [form] = Form.useForm<TemplateDesignerValues>();
  const [saving, setSaving] = useState(false);
  const [assembly, setAssembly] = useState<Assembly>(() =>
    source ? cloneJSON(source.assembly) : { nodes: [], edges: [] },
  );

  const save = async () => {
    let values: TemplateDesignerValues;
    try {
      values = await form.validateFields();
    } catch {
      // AntD has already placed the localized field errors next to their controls.
      return;
    }
    const prepared = prepareAssemblyForSave(assembly);
    if (prepared.invalidNodeId) {
      const node = assembly.nodes.find((item) => item.id === prepared.invalidNodeId);
      message.error(
        t("business.errors.invalidAssetJson", {
          name: node?.name ?? prepared.invalidNodeId,
        }),
      );
      return;
    }

    setSaving(true);
    try {
      const created = await createBusinessTemplate({
        schema_version: "nereidlink.business-template/v1",
        id: values.id.trim(),
        version: values.version.trim(),
        name: values.name.trim(),
        description: values.description.trim(),
        requires: source ? cloneJSON(source.requires) : {},
        parameters: JSON.parse(values.parametersSchema) as Record<string, unknown>,
        assembly: prepared.assembly,
      });
      onCreated(created);
      message.success(
        t(editing ? "business.templates.versionCreated" : "business.templates.created"),
      );
      onClose();
    } catch (error) {
      message.error(extractError(error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer
      open
      width="96vw"
      title={t(editing ? "business.templates.editTitle" : "business.templates.createTitle")}
      onClose={onClose}
      footer={
        <Space style={{ display: "flex", justifyContent: "flex-end" }}>
          <Button onClick={onClose} disabled={saving}>
            {t("business.actions.cancel")}
          </Button>
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={saving}
            onClick={() => void save()}
          >
            {t(editing ? "business.templates.saveVersion" : "business.templates.create")}
          </Button>
        </Space>
      }
    >
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t(editing ? "business.templates.editTitle" : "business.templates.createTitle")}
        description={t(
          editing
            ? "business.templates.editDescription"
            : "business.templates.createDescription",
        )}
      />
      <Form
        form={form}
        layout="vertical"
        initialValues={{
          id: editing ? source?.id : "",
          version: editing ? nextTemplateVersion(source?.version ?? "1.0.0") : "1.0.0",
          name: editing
            ? source?.name
            : source
              ? t("business.templates.copyName", { name: source.name })
              : "",
          description: source?.description ?? "",
          parametersSchema: JSON.stringify(
            source?.parameters ?? { type: "object", properties: {} },
            null,
            2,
          ),
        }}
      >
        <Row gutter={16}>
          <Col xs={24} md={12} xl={6}>
            <Form.Item
              name="name"
              label={t("business.templates.templateName")}
              rules={[
                {
                  required: true,
                  whitespace: true,
                  message: t("business.templates.templateNameRequired"),
                },
              ]}
            >
              <Input maxLength={255} />
            </Form.Item>
          </Col>
          <Col xs={24} md={12} xl={6}>
            <Form.Item
              name="id"
              label={t("business.templates.templateId")}
              extra={editing ? t("business.templates.templateIdImmutableHint") : undefined}
              rules={[
                {
                  required: true,
                  message: t("business.templates.templateIdRequired"),
                },
                {
                  pattern: /^[a-z0-9][a-z0-9._-]{0,127}$/,
                  message: t("business.templates.templateIdInvalid"),
                },
              ]}
            >
              <Input
                maxLength={128}
                disabled={editing}
                placeholder={t("business.templates.templateIdPlaceholder")}
              />
            </Form.Item>
          </Col>
          <Col xs={24} md={12} xl={6}>
            <Form.Item
              name="version"
              label={t("business.templates.version")}
              rules={[
                {
                  required: true,
                  whitespace: true,
                  message: t("business.templates.versionRequired"),
                },
              ]}
            >
              <Input maxLength={64} />
            </Form.Item>
          </Col>
          <Col xs={24} md={12} xl={6}>
            <Form.Item name="description" label={t("business.templates.description")}>
              <Input maxLength={1000} />
            </Form.Item>
          </Col>
        </Row>
        <Form.Item
          name="parametersSchema"
          label={t("business.templates.parameterSchema")}
          extra={t("business.templates.parameterSchemaHint")}
          rules={[
            {
              validator: (_, value: string) => {
                try {
                  const parsed = JSON.parse(value) as unknown;
                  if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                    return Promise.resolve();
                  }
                } catch {
                  // The localized rejection covers malformed and non-object JSON.
                }
                return Promise.reject(
                  new Error(t("business.templates.invalidParameterSchema")),
                );
              },
            },
          ]}
        >
          <Input.TextArea rows={5} />
        </Form.Item>
      </Form>

      <Typography.Paragraph type="secondary">
        {t("business.templates.assemblyHint")}
      </Typography.Paragraph>
      <BusinessCanvas
        assembly={assembly}
        issues={[]}
        parameterSchema={{}}
        parameters={{}}
        showParameters={false}
        onAssemblyChange={setAssembly}
        onParametersChange={() => undefined}
      />
    </Drawer>
  );
}
