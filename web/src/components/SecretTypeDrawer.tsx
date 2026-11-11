// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretTypeDrawer creates or edits an edge secret type descriptor
// (ADR-0034). It offers two authoring modes: a field-list builder that
// generates a JSON Schema, and an advanced raw JSON Schema editor. Edge
// descriptors are read-only: the drawer shows them with a clone action.

import { useState } from "react";
import {
  App,
  Button,
  Divider,
  Drawer,
  Form,
  Input,
  Segmented,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from "antd";
import { useTranslation } from "react-i18next";

import type { SecretType, SecretTypeInput } from "../api/secretTypes";
import {
  cloneSecretType,
  createSecretType,
  updateSecretType,
} from "../api/secretTypes";
import { extractError } from "../api/client";
import { extractFields } from "./schema/schemaFieldsUtils";

// BuilderFieldKind is the control selectable in the builder.
type BuilderFieldKind = "string" | "password" | "integer" | "number" | "boolean" | "select";

// BuilderField is one row in the field-list builder.
interface BuilderField {
  key: string;
  labelEn: string;
  labelZh: string;
  kind: BuilderFieldKind;
  required: boolean;
  options?: string;
}

// fieldKindOptions lists the builder control choices.
const fieldKindOptions: { value: BuilderFieldKind; labelKey: string }[] = [
  { value: "string", labelKey: "pages.secretTypes.builder.kinds.string" },
  { value: "password", labelKey: "pages.secretTypes.builder.kinds.password" },
  { value: "integer", labelKey: "pages.secretTypes.builder.kinds.integer" },
  { value: "number", labelKey: "pages.secretTypes.builder.kinds.number" },
  { value: "boolean", labelKey: "pages.secretTypes.builder.kinds.boolean" },
  { value: "select", labelKey: "pages.secretTypes.builder.kinds.select" },
];

// buildSchema converts builder rows into a draft 2020-12 JSON Schema.
function buildSchema(fields: BuilderField[]): Record<string, unknown> {
  const properties: Record<string, Record<string, unknown>> = {};
  const required: string[] = [];
  for (const field of fields) {
    if (!field.key) continue;
    const prop: Record<string, unknown> = {
      title: field.labelEn || field.key,
      "x-labels": { en: field.labelEn || field.key, zh: field.labelZh || field.key },
    };
    switch (field.kind) {
      case "password":
        prop.type = "string";
        prop.format = "password";
        break;
      case "integer":
        prop.type = "integer";
        break;
      case "number":
        prop.type = "number";
        break;
      case "boolean":
        prop.type = "boolean";
        break;
      case "select":
        prop.type = "string";
        prop.enum = (field.options ?? "")
          .split(",")
          .map((option) => option.trim())
          .filter(Boolean);
        break;
      default:
        prop.type = "string";
    }
    properties[field.key] = prop;
    if (field.required) required.push(field.key);
  }
  const schema: Record<string, unknown> = { type: "object", properties };
  if (required.length > 0) schema.required = required;
  return schema;
}

// schemaToFields reverse-parses a saved JSON Schema into builder rows so a
// manual descriptor can be edited in the field-list builder. It is the
// inverse of buildSchema for the flat schemas the builder produces.
function schemaToFields(
  schema: Record<string, unknown>,
): BuilderField[] {
  const object = schema as {
    properties?: Record<
      string,
      Record<string, unknown> & {
        enum?: unknown[];
        "x-labels"?: { en?: string; zh?: string };
        "x-secret"?: boolean;
      }
    >;
    required?: string[];
  };
  if (object.properties === undefined) return [];
  const required = new Set(object.required ?? []);
  const rows: BuilderField[] = [];
  for (const [key, prop] of Object.entries(object.properties)) {
    const labels = prop["x-labels"] ?? {};
    let kind: BuilderFieldKind = "string";
    let options: string | undefined;
    if (Array.isArray(prop.enum) && prop.enum.length > 0) {
      kind = "select";
      options = prop.enum.map((value) => String(value)).join(", ");
    } else if (prop.format === "password" || prop["x-secret"] === true) {
      kind = "password";
    } else if (prop.type === "integer") {
      kind = "integer";
    } else if (prop.type === "number") {
      kind = "number";
    } else if (prop.type === "boolean") {
      kind = "boolean";
    }
    rows.push({
      key,
      labelEn: labels.en ?? (typeof prop.title === "string" ? prop.title : ""),
      labelZh: labels.zh ?? "",
      kind,
      required: required.has(key),
      options,
    });
  }
  return rows;
}

// DrawerMode distinguishes the three ways a descriptor can be opened.
type DrawerMode = "create" | "view" | "edit";

interface SecretTypeDrawerProps {
  open: boolean;
  mode: DrawerMode;
  descriptor?: SecretType;
  onClose: () => void;
  onSaved: () => void;
  // onSwitchEdit moves a manual descriptor from read-only view to editing
  // without closing the drawer.
  onSwitchEdit: () => void;
}

// SecretTypeDrawer renders the descriptor create/view/edit drawer.
export default function SecretTypeDrawer({
  open,
  mode: drawerMode,
  descriptor,
  onClose,
  onSaved,
  onSwitchEdit,
}: SecretTypeDrawerProps) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const [form] = Form.useForm();
  const [mode, setMode] = useState<"builder" | "advanced">("builder");
  const [rawSchema, setRawSchema] = useState("");
  const [rows, setRows] = useState<BuilderField[]>([]);
  const [saving, setSaving] = useState(false);
  // View is always read-only, for both edge-advertised and manual rows;
  // only explicit edit/create show the authoring form.
  const readOnly = drawerMode === "view";
  const isEdit = drawerMode === "edit";

  // Populate after the drawer finishes opening. The body is force-rendered
  // (destroyOnHidden below), so filling the form here — rather than in an
  // effect that may run before the form fields register — guarantees the
  // header inputs and builder rows are present on first paint.
  const onAfterOpenChange = (visible: boolean) => {
    if (!visible) return;
    setMode("builder");
    if (descriptor) {
      form.setFieldsValue({
        type: descriptor.secret_type,
        type_version: descriptor.type_version,
        labelEn: descriptor.display_name?.en ?? "",
        labelZh: descriptor.display_name?.zh ?? "",
      });
      setRows(schemaToFields(descriptor.fields_schema));
      setRawSchema(JSON.stringify(descriptor.fields_schema, null, 2));
    } else {
      form.resetFields();
      setRows([]);
      setRawSchema("");
    }
  };

  // toInput reads the form and selected mode into an API payload. It returns
  // null when the schema section is empty or invalid.
  const toInput = async (): Promise<SecretTypeInput | null> => {
    const values = await form.validateFields();
    let fieldsSchema: Record<string, unknown>;
    if (mode === "builder") {
      fieldsSchema = buildSchema(rows);
      if (Object.keys(fieldsSchema.properties as object).length === 0) {
        return null;
      }
    } else {
      if (!isValidJSON(rawSchema)) return null;
      fieldsSchema = JSON.parse(rawSchema) as Record<string, unknown>;
    }
    return {
      type: values.type,
      type_version: values.type_version ?? "",
      fields_schema: fieldsSchema,
      display_name: { en: values.labelEn ?? "", zh: values.labelZh ?? "" },
    };
  };

  const onSave = async () => {
    const input = await toInput();
    if (!input) {
      message.warning(t("pages.secretTypes.builder.atLeastOne"));
      return;
    }
    setSaving(true);
    try {
      if (isEdit && descriptor) {
        await updateSecretType(descriptor.secret_type, descriptor.type_version, input);
      } else {
        await createSecretType(input);
      }
      onSaved();
    } catch (error) {
      form.setFields([{ name: "type", errors: [extractError(error).message] }]);
    } finally {
      setSaving(false);
    }
  };

  const onClone = async () => {
    if (!descriptor) return;
    setSaving(true);
    try {
      await cloneSecretType(
        descriptor.secret_type,
        descriptor.type_version,
        `${descriptor.secret_type}-copy`,
      );
      onSaved();
    } catch (error) {
      form.setFields([{ name: "type", errors: [extractError(error).message] }]);
    } finally {
      setSaving(false);
    }
  };

  return (
    <Drawer
      width={920}
      open={open}
      onClose={onClose}
      title={
        readOnly
          ? t("pages.secretTypes.titles.view")
          : isEdit
            ? t("pages.secretTypes.titles.edit")
            : t("pages.secretTypes.titles.create")
      }
      extra={
        readOnly ? (
          descriptor?.source === "edge" ? (
            <Button type="primary" onClick={onClone} loading={saving}>
              {t("pages.secretTypes.clone")}
            </Button>
          ) : (
            <Button type="primary" onClick={onSwitchEdit}>
              {t("common.edit")}
            </Button>
          )
        ) : (
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="primary" onClick={onSave} loading={saving}>
              {t("common.save")}
            </Button>
          </Space>
        )
      }
      destroyOnHidden
      afterOpenChange={onAfterOpenChange}
    >
      <Form form={form} layout="vertical" preserve={false}>
        <Space size="middle" style={{ display: "flex" }} wrap>
          <Form.Item
            name="type"
            label={t("pages.secretTypes.form.type")}
            rules={[{ required: true }]}
            style={{ minWidth: 200 }}
          >
            <Input disabled={readOnly || isEdit} />
          </Form.Item>
          <Form.Item name="type_version" label={t("pages.secretTypes.form.version")}>
            <Input disabled={readOnly} style={{ width: 140 }} />
          </Form.Item>
          <Form.Item name="labelEn" label={t("pages.secretTypes.form.labelEn")}>
            <Input disabled={readOnly} style={{ width: 180 }} />
          </Form.Item>
          <Form.Item name="labelZh" label={t("pages.secretTypes.form.labelZh")}>
            <Input disabled={readOnly} style={{ width: 180 }} />
          </Form.Item>
        </Space>

        {!readOnly && (
          <>
            <Divider style={{ margin: "8px 0 16px" }} />
            <Segmented
              value={mode}
              onChange={(value) => setMode(value as "builder" | "advanced")}
              options={[
                { value: "builder", label: t("pages.secretTypes.modes.builder") },
                { value: "advanced", label: t("pages.secretTypes.modes.advanced") },
              ]}
            />
            {mode === "builder" ? (
              <BuilderEditor t={t} rows={rows} setRows={setRows} />
            ) : (
              <AdvancedEditor value={rawSchema} onChange={setRawSchema} t={t} />
            )}
          </>
        )}
      </Form>

      {readOnly && descriptor && (
        <SchemaOverview
          t={t}
          schema={descriptor.fields_schema}
          source={descriptor.source}
        />
      )}
    </Drawer>
  );
}

// SchemaOverviewProps carries the translator, schema and row source.
interface SchemaOverviewProps {
  t: (key: string) => string;
  schema: Record<string, unknown>;
  source: SecretType["source"];
}

// SchemaOverview renders a descriptor as a read-only table of its fields
// (key, control kind, required marker and label). It is used for both
// edge-advertised descriptors and for viewing a manual descriptor.
function SchemaOverview({ t, schema, source }: SchemaOverviewProps) {
  const fields = extractFields(schema);
  return (
    <div style={{ marginTop: 8 }}>
      <Table
        rowKey="key"
        size="small"
        pagination={false}
        dataSource={fields}
        columns={[
          {
            title: t("pages.secretTypes.overview.key"),
            dataIndex: "key",
            render: (value: string) => <Typography.Text strong>{value}</Typography.Text>,
          },
          {
            title: t("pages.secretTypes.overview.kind"),
            dataIndex: "kind",
            width: 120,
            render: (kind: string) => (
              <Tag>{t(`pages.secretTypes.builder.kinds.${kind}`)}</Tag>
            ),
          },
          {
            title: t("pages.secretTypes.overview.required"),
            dataIndex: "required",
            width: 100,
            render: (required: boolean) =>
              required ? (
                <Tag color="red">{t("pages.secretTypes.overview.yes")}</Tag>
              ) : (
                <Typography.Text type="secondary">
                  {t("pages.secretTypes.overview.no")}
                </Typography.Text>
              ),
          },
          {
            title: t("pages.secretTypes.overview.label"),
            key: "label",
            render: (_: unknown, field: { label: string }) => field.label,
          },
        ]}
      />
      {source === "edge" && (
        <Typography.Paragraph type="secondary" style={{ marginTop: 12 }}>
          {t("pages.secretTypes.edgeReadOnly")}
        </Typography.Paragraph>
      )}
    </div>
  );
}

// BuilderEditorProps carries the translator.
interface BuilderEditorProps {
  t: (key: string) => string;
  rows: BuilderField[];
  setRows: (rows: BuilderField[]) => void;
}

// emptyRow is the shape added when the operator clicks "add field".
const emptyRow: BuilderField = {
  key: "",
  labelEn: "",
  labelZh: "",
  kind: "string",
  required: false,
  options: "",
};

// BuilderEditor renders the editable field-list builder. Rows live in the
// parent's local state (not a nested Form.List), which makes back-filling a
// descriptor and registering fields independent of form-mount timing.
function BuilderEditor({ t, rows, setRows }: BuilderEditorProps) {
  // patch updates one field of the row at index.
  const patch = (index: number, change: Partial<BuilderField>) => {
    setRows(rows.map((row, i) => (i === index ? { ...row, ...change } : row)));
  };
  return (
    <div style={{ marginTop: 16 }}>
      {rows.map((row, index) => (
        <Space
          key={index}
          align="baseline"
          style={{ display: "flex", marginBottom: 8 }}
          wrap
        >
          <Input
            value={row.key}
            placeholder={t("pages.secretTypes.builder.fieldKey")}
            style={{ width: 140 }}
            onChange={(event) => patch(index, { key: event.target.value })}
          />
          <Select
            value={row.kind}
            style={{ width: 120 }}
            options={fieldKindOptions.map((option) => ({
              value: option.value,
              label: t(option.labelKey),
            }))}
            onChange={(kind: BuilderFieldKind) => patch(index, { kind })}
          />
          <Input
            value={row.labelEn}
            placeholder={t("pages.secretTypes.builder.labelEn")}
            style={{ width: 140 }}
            onChange={(event) => patch(index, { labelEn: event.target.value })}
          />
          <Input
            value={row.labelZh}
            placeholder={t("pages.secretTypes.builder.labelZh")}
            style={{ width: 140 }}
            onChange={(event) => patch(index, { labelZh: event.target.value })}
          />
          <Switch
            checked={row.required}
            checkedChildren={t("pages.secretTypes.builder.required")}
            onChange={(required) => patch(index, { required })}
          />
          <Input
            value={row.options}
            placeholder={t("pages.secretTypes.builder.options")}
            style={{ width: 160 }}
            onChange={(event) => patch(index, { options: event.target.value })}
          />
          <Button
            danger
            type="text"
            onClick={() => setRows(rows.filter((_, i) => i !== index))}
          >
            {t("common.delete")}
          </Button>
        </Space>
      ))}
      <Button type="dashed" onClick={() => setRows([...rows, { ...emptyRow }])} block>
        {t("pages.secretTypes.builder.addField")}
      </Button>
    </div>
  );
}

// AdvancedEditorProps carries the raw schema text state.
interface AdvancedEditorProps {
  value: string;
  onChange: (value: string) => void;
  t: (key: string) => string;
}

// AdvancedEditor renders the raw JSON Schema text area.
function AdvancedEditor({ value, onChange, t }: AdvancedEditorProps) {
  const valid = isValidJSON(value);
  return (
    <Form.Item
      style={{ marginTop: 16, marginBottom: 0 }}
      label={t("pages.secretTypes.advanced.schema")}
      validateStatus={valid ? "" : "error"}
      help={valid ? "" : t("pages.secretTypes.advanced.invalid")}
    >
      <Input.TextArea
        rows={16}
        value={value}
        onChange={(event) => onChange(event.target.value)}
        style={{ fontFamily: "monospace" }}
      />
    </Form.Item>
  );
}

// isValidJSON reports whether text parses. Empty text is treated as invalid.
function isValidJSON(text: string): boolean {
  if (!text.trim()) return false;
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}
