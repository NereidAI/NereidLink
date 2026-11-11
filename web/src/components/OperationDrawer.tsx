// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// OperationDrawer creates or edits one private gateway operation
// descriptor. Descriptor fields are plain controls; the parameter schema can
// be built field-by-field or edited as raw JSON in advanced mode.
// Edge-advertised descriptors are edited only after cloning, never directly.

import {
  Button,
  Checkbox,
  Drawer,
  Form,
  Input,
  InputNumber,
  Radio,
  Select,
  Space,
  Table,
} from "antd";
import { useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import { useMutation } from "@tanstack/react-query";

import {
  createOperation,
  updateOperation,
  type OperationInput,
  type OperationView,
} from "../api/gatewayOperations";
import { extractError } from "../api/client";

interface OperationDrawerProps {
  open: boolean;
  target?: OperationView | null;
  onClose: () => void;
  onSaved: () => void;
}

// OperationDrawer renders the private-descriptor form.
export default function OperationDrawer({ open, target, onClose, onSaved }: OperationDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm();

  useEffect(() => {
    if (open) {
      form.resetFields();
      if (target) {
        form.setFieldsValue({
          operation: target.operation,
          type_version: target.type_version,
          interaction: target.interaction,
          timeout_ms: target.timeout_ms,
          risk: target.risk,
          display_en: target.display_name?.en,
          display_zh: target.display_name?.zh,
          desc_en: target.description?.en,
          desc_zh: target.description?.zh,
          param_schema: JSON.stringify(target.param_schema ?? {}, null, 2),
          result_schema: JSON.stringify(target.result_schema ?? {}, null, 2),
        });
      }
    }
  }, [open, target, form]);

  const mutation = useMutation({
    mutationFn: async () => {
      const values = await form.validateFields();
      const input: OperationInput = {
        operation: values.operation,
        type_version: values.type_version ?? "",
        interaction: values.interaction,
        timeout_ms: values.timeout_ms,
        risk: values.risk,
        display_name: { en: values.display_en, zh: values.display_zh },
        description: { en: values.desc_en, zh: values.desc_zh },
        param_schema: parseSchema(values.param_schema),
        result_schema: parseSchema(values.result_schema),
      };
      if (target) {
        return updateOperation(target.operation, target.type_version, input);
      }
      return createOperation(input);
    },
    onSuccess: () => {
      onSaved();
      onClose();
    },
    onError: (error) => {
      form.setFields([
        { name: "operation", errors: [extractError(error).message] },
      ]);
    },
  });

  return (
    <Drawer
      title={target ? t("pages.operations.titles.edit") : t("pages.operations.titles.create")}
      width={620}
      open={open}
      onClose={onClose}
      destroyOnClose
      extra={
        <Space>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button type="primary" loading={mutation.isPending} onClick={() => mutation.mutate()}>
            {t("common.save")}
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        initialValues={{
          interaction: "request-response",
          timeout_ms: 15000,
          risk: "normal",
          param_schema: "{}",
          result_schema: "{}",
        }}
      >
        <Form.Item name="operation" label={t("pages.operations.form.operation")} rules={[{ required: true }]}>
          <Input />
        </Form.Item>
        <Form.Item name="type_version" label={t("pages.operations.form.version")}>
          <Input />
        </Form.Item>
        <Space size="middle" wrap>
          <Form.Item name="interaction" label={t("pages.operations.form.interaction")}>
            <Select
              style={{ width: 180 }}
              options={[
                { value: "request-response", label: t("pages.operations.interaction.rr") },
                { value: "one-way", label: t("pages.operations.interaction.ow") },
              ]}
            />
          </Form.Item>
          <Form.Item name="risk" label={t("pages.operations.form.risk")}>
            <Select
              style={{ width: 160 }}
              options={["read", "normal", "dangerous"].map((risk) => ({
                value: risk,
                label: t(`pages.operations.risks.${risk}`),
              }))}
            />
          </Form.Item>
          <Form.Item name="timeout_ms" label={t("pages.operations.form.timeout")}>
            <InputNumber min={100} max={60000} addonAfter="ms" />
          </Form.Item>
        </Space>
        <Form.Item name="display_en" label={t("pages.operations.form.labelEn")}>
          <Input />
        </Form.Item>
        <Form.Item name="display_zh" label={t("pages.operations.form.labelZh")}>
          <Input />
        </Form.Item>
        <Form.Item name="desc_en" label={t("pages.operations.form.descEn")}>
          <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} />
        </Form.Item>
        <Form.Item name="desc_zh" label={t("pages.operations.form.descZh")}>
          <Input.TextArea autoSize={{ minRows: 1, maxRows: 3 }} />
        </Form.Item>
        <Form.Item
          name="param_schema"
          label={t("pages.operations.form.paramSchema")}
          rules={[{ validator: (_: unknown, value: string) => validateJSON(value) }]}
        >
          <ParamSchemaBuilder />
        </Form.Item>
        <Form.Item
          name="result_schema"
          label={t("pages.operations.form.resultSchema")}
          rules={[{ validator: (_: unknown, value: string) => validateJSON(value) }]}
        >
          <Input.TextArea rows={6} />
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// paramTypes are the JSON Schema primitive types the structured builder emits.
const paramTypes = ["string", "integer", "number", "boolean", "array", "object"];

// ParamRow is one structured parameter field.
interface ParamRow {
  key: string;
  type: string;
  required: boolean;
  description: string;
}

// ParamSchemaBuilder toggles between a field table and raw JSON editing.
// The value contract with Form.Item is a schema JSON string.
function ParamSchemaBuilder({
  value = "{}",
  onChange,
}: {
  value?: string;
  onChange?: (next: string) => void;
}) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<"fields" | "json">(() =>
    rowsFromSchema(value) === null ? "json" : "fields",
  );
  const [rows, setRows] = useState<ParamRow[]>(() => rowsFromSchema(value) ?? []);
  // seen is the last schema string reflected by rows; comparing during
  // render (React's documented prev-prop adjustment) seeds external updates
  // such as form reset / clone without an effect.
  const [seen, setSeen] = useState(value);
  if (mode === "fields" && value !== seen) {
    const parsed = rowsFromSchema(value);
    if (parsed) {
      setRows(parsed);
      setSeen(value);
    }
  }

  const emitRows = (next: ParamRow[]) => {
    setRows(next);
    const text = schemaTextFromRows(next);
    setSeen(text);
    onChange?.(text);
  };

  const switchMode = (next: "fields" | "json") => {
    if (next === "fields") {
      const parsed = rowsFromSchema(value);
      if (!parsed) {
        return;
      }
      setRows(parsed);
    }
    setMode(next);
  };

  if (mode === "json") {
    const canBuild = rowsFromSchema(value) !== null;
    return (
      <Space direction="vertical" style={{ width: "100%" }} size="small">
        <Radio.Group
          size="small"
          optionType="button"
          buttonStyle="solid"
          value={mode}
          onChange={(e) => switchMode(e.target.value)}
          options={[
            {
              value: "fields",
              label: t("pages.operations.schema.fields"),
              disabled: !canBuild,
              title: canBuild ? undefined : t("pages.operations.schema.advancedOnly"),
            },
            { value: "json", label: t("pages.operations.schema.advanced") },
          ]}
        />
        {!canBuild && (
          <span style={{ color: "rgba(0,0,0,0.45)", fontSize: 12 }}>
            {t("pages.operations.schema.advancedOnly")}
          </span>
        )}
        <Input.TextArea
          rows={8}
          value={value}
          onChange={(e) => onChange?.(e.target.value)}
        />
      </Space>
    );
  }

  const patch = (index: number, next: Partial<ParamRow>) =>
    emitRows(rows.map((r, i) => (i === index ? { ...r, ...next } : r)));

  const columns = [
    {
      title: t("pages.operations.schema.colName"),
      dataIndex: "key",
      width: 150,
      render: (_: unknown, _row: ParamRow, index: number) => (
        <Input
          size="small"
          value={rows[index].key}
          onChange={(e) => patch(index, { key: e.target.value })}
        />
      ),
    },
    {
      title: t("pages.operations.schema.colType"),
      dataIndex: "type",
      width: 120,
      render: (_: unknown, _row: ParamRow, index: number) => (
        <Select
          size="small"
          style={{ width: 110 }}
          value={rows[index].type}
          options={paramTypes.map((type) => ({ value: type, label: type }))}
          onChange={(type) => patch(index, { type })}
        />
      ),
    },
    {
      title: t("pages.operations.schema.colRequired"),
      dataIndex: "required",
      width: 80,
      render: (_: unknown, _row: ParamRow, index: number) => (
        <Checkbox
          checked={rows[index].required}
          onChange={(e) => patch(index, { required: e.target.checked })}
        />
      ),
    },
    {
      title: t("pages.operations.schema.colDescription"),
      dataIndex: "description",
      render: (_: unknown, _row: ParamRow, index: number) => (
        <Input
          size="small"
          value={rows[index].description}
          onChange={(e) => patch(index, { description: e.target.value })}
        />
      ),
    },
    {
      title: "",
      width: 40,
      render: (_: unknown, _row: ParamRow, index: number) => (
        <Button
          size="small"
          type="text"
          danger
          onClick={() => emitRows(rows.filter((_, i) => i !== index))}
        >
          {t("pages.operations.schema.remove")}
        </Button>
      ),
    },
  ];

  return (
    <Space direction="vertical" style={{ width: "100%" }} size="small">
      <Radio.Group
        size="small"
        optionType="button"
        buttonStyle="solid"
        value={mode}
        onChange={(e) => switchMode(e.target.value)}
        options={[
          { value: "fields", label: t("pages.operations.schema.fields") },
          { value: "json", label: t("pages.operations.schema.advanced") },
        ]}
      />
      <Table
        rowKey={(_, index) => String(index)}
        size="small"
        columns={columns}
        dataSource={rows}
        pagination={false}
      />
      <Button size="small" onClick={() => emitRows([...rows, { key: "", type: "string", required: false, description: "" }])}>
        {t("pages.operations.schema.add")}
      </Button>
    </Space>
  );
}

// parseSchema parses a JSON text into an object.
function parseSchema(raw: string): Record<string, unknown> {
  return JSON.parse(raw) as Record<string, unknown>;
}

// validateJSON rejects non-object JSON text.
async function validateJSON(raw: string): Promise<void> {
  const value = JSON.parse(raw) as unknown;
  if (typeof value !== "object" || value === null) {
    throw new Error("JSON object required");
  }
}

// rowsFromSchema decodes a builder-compatible object schema into rows. A
// schema using any construct beyond {type,properties,required}/{type,
// description} returns null so advanced mode is forced instead of dropping
// data.
function rowsFromSchema(raw: string): ParamRow[] | null {
  let obj: Record<string, unknown>;
  try {
    const parsed = JSON.parse(raw) as unknown;
    if (typeof parsed !== "object" || parsed === null) {
      return null;
    }
    obj = parsed as Record<string, unknown>;
  } catch {
    return null;
  }
  const rootKeys = Object.keys(obj);
  if (rootKeys.length === 0) {
    return [];
  }
  // $schema is the standard dialect annotation advertised gateways emit;
  // the field builder ignores it (it cannot represent it) instead of
  // forcing the whole schema into advanced JSON.
  for (const key of rootKeys) {
    if (key !== "type" && key !== "properties" && key !== "required" && key !== "$schema") {
      return null;
    }
  }
  if (obj.type !== undefined && obj.type !== "object") {
    return null;
  }
  const props = (obj.properties ?? {}) as Record<string, Record<string, unknown>>;
  if (typeof props !== "object" || props === null) {
    return null;
  }
  const required = new Set(
    Array.isArray(obj.required) ? (obj.required as unknown[]).map(String) : [],
  );
  const rows: ParamRow[] = [];
  for (const [key, def] of Object.entries(props)) {
    if (typeof def !== "object" || def === null) {
      return null;
    }
    for (const dk of Object.keys(def)) {
      if (dk !== "type" && dk !== "description") {
        return null;
      }
    }
    rows.push({
      key,
      type: typeof def.type === "string" ? def.type : "string",
      required: required.has(key),
      description: typeof def.description === "string" ? def.description : "",
    });
  }
  return rows;
}

// schemaTextFromRows encodes rows into pretty-printed object-schema JSON.
function schemaTextFromRows(rows: ParamRow[]): string {
  if (rows.length === 0) {
    return "{}";
  }
  const properties: Record<string, Record<string, unknown>> = {};
  const required: string[] = [];
  for (const row of rows) {
    const key = row.key.trim();
    if (!key) {
      continue;
    }
    const def: Record<string, unknown> = { type: row.type };
    if (row.description.trim()) {
      def.description = row.description.trim();
    }
    properties[key] = def;
    if (row.required) {
      required.push(key);
    }
  }
  const schema: Record<string, unknown> = { type: "object", properties };
  if (required.length > 0) {
    schema.required = required;
  }
  return JSON.stringify(schema, null, 2);
}
