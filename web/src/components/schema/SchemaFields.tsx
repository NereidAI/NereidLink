// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SchemaFields renders the top-level properties of a draft 2020-12 JSON
// Schema as AntD form controls, to be placed inside a <Form>. The kernel
// understands no protocol: the same component drives credential-value
// forms and later gateway-operation parameter forms.
//
// Only a flat object schema is rendered: string/password, number/integer,
// boolean and enum selectors. Field models are extracted by the pure
// helpers in schemaFieldsUtils.ts; this file exports only the component so
// the React Fast Refresh boundary stays intact.

import { Form, Input, InputNumber, Select, Switch, Tooltip, Typography } from "antd";
import { InfoCircleOutlined } from "@ant-design/icons";
import type { ReactNode } from "react";

import { extractFields, type SchemaField } from "./schemaFieldsUtils";

interface SchemaFieldsProps {
  schema: Record<string, unknown>;
  disabled?: boolean;
  // namePrefix nests every rendered field under this form path so the
  // resulting values are collected under values[namePrefix[0]]... rather
  // than at the form root. A plain <Form.Item name=...> wrapping custom
  // fields does NOT forward a name prefix; only an explicit NamePath does.
  namePrefix?: (string | number)[];
}

// fieldLabel renders the label, required marker is handled by Form.Item.
function fieldLabel(field: SchemaField): ReactNode {
  if (!field.description) return field.label;
  return (
    <span>
      {field.label}{" "}
      <Tooltip title={field.description}>
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          <InfoCircleOutlined />
        </Typography.Text>
      </Tooltip>
    </span>
  );
}

// SchemaFields renders the typed form controls for one object schema.
export default function SchemaFields({ schema, disabled, namePrefix }: SchemaFieldsProps) {
  const fields = extractFields(schema);
  return (
    <>
      {fields.map((field) => (
        <Form.Item
          key={field.key}
          name={namePrefix ? [...namePrefix, field.key] : field.key}
          label={fieldLabel(field)}
          rules={field.required ? [{ required: true }] : undefined}
          initialValue={field.defaultValue}
        >
          {renderControl(field, disabled)}
        </Form.Item>
      ))}
    </>
  );
}

// renderControl selects the AntD control for one field.
function renderControl(field: SchemaField, disabled?: boolean): ReactNode {
  switch (field.kind) {
    case "password":
      return <Input.Password autoComplete="new-password" disabled={disabled} />;
    case "number":
      return <InputNumber style={{ width: "100%" }} disabled={disabled} />;
    case "boolean":
      return <Switch disabled={disabled} />;
    case "select":
      return <Select options={field.options} disabled={disabled} />;
    default:
      return <Input disabled={disabled} />;
  }
}
