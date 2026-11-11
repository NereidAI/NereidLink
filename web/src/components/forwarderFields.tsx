// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// forwarderFields.tsx renders one per-sink configuration field under the
// "cfg" form prefix. The declarative field descriptors and summary helpers
// live in forwarderFieldConfig.ts so this file exports only the component,
// which keeps the React Fast Refresh boundary intact.

import type { ReactNode } from "react";
import { Form, Input, InputNumber, Select, Switch } from "antd";

import type { SinkField } from "./forwarderFieldConfig";

// FieldProps carries the render-time dependencies shared by every field.
interface FieldProps {
  // field is the descriptor.
  field: SinkField;
  // t translates labels/hints.
  t: (k: string) => string;
}

// SinkConfigField renders one descriptor under the "cfg" form prefix, so its
// value nests under sink_config automatically.
export function SinkConfigField({ field, t }: FieldProps): ReactNode {
  const name = ["cfg", field.key];
  const label = t(field.labelKey);
  const tooltip = field.hintKey ? t(field.hintKey) : undefined;
  const form = Form.useFormInstance();
  const gateValue = Form.useWatch(
    field.showIf ? ["cfg", field.showIf] : ["cfg", "__always_visible__"],
    form,
  );
  if (field.showIf && !gateValue) {
    return null;
  }

  let control: ReactNode;
  switch (field.type) {
    case "text":
      control = <Input placeholder={field.placeholder} autoComplete="off" />;
      break;
    case "password":
      control = <Input.Password autoComplete="new-password" />;
      break;
    case "textarea":
      control = (
        <Input.TextArea rows={field.rows ?? 3} placeholder={field.placeholder} />
      );
      break;
    case "number":
      control = (
        <InputNumber min={field.min} max={field.max} style={{ width: "100%" }} />
      );
      break;
    case "select":
      control = (
        <Select
          options={field.options?.map((opt) => ({
            value: opt.value,
            label: opt.labelKey ? t(opt.labelKey) : opt.label,
          }))}
          onChange={(value) => field.afterSelect?.(form, value)}
        />
      );
      break;
    case "switch":
      control = <Switch />;
      break;
  }

  return (
    <Form.Item
      name={name}
      label={label}
      tooltip={tooltip}
      valuePropName={field.type === "switch" ? "checked" : "value"}
      rules={[
        field.required
          ? { required: true, message: t("pages.forwarders.form.fieldRequired") }
          : null,
        field.requiredIf
          ? {
              validator: async (_: unknown, value: unknown) => {
                const gate = form.getFieldValue([
                  "cfg",
                  field.requiredIf,
                ]);
                if (
                  gate &&
                  (value === undefined || value === null || value === "")
                ) {
                  throw new Error(
                    t("pages.forwarders.form.fieldRequiredWhenAuth"),
                  );
                }
              },
            }
          : null,
      ].filter((rule): rule is NonNullable<typeof rule> => rule !== null)}
    >
      {control}
    </Form.Item>
  );
}
