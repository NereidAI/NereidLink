// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DictFields renders structured inputs for catalog FieldSpecs (connection
// parameters and per-point address parameters, ADR-0033). It is pure UI:
// callers own the JSON object and persist changes through onChange. Labels
// are literal strings stored in the global catalog, so this component is
// translation-free.
//
// Each control is wrapped in a plain div rather than an HTML <label>: antd
// inputs render their own ids only inside Form.Item, so a <label> without a
// matching htmlFor triggers browser a11y warnings and does nothing useful.

import { Input, InputNumber, Select, Switch, Tooltip } from "antd";
import { QuestionCircleOutlined } from "@ant-design/icons";

import type { FieldSpec } from "../../api/protocolCatalog";

// DictFieldInputProps configures one field input.
interface DictFieldInputProps {
  // spec describes the parameter (key, kind, options, bounds).
  spec: FieldSpec;
  // value is the current JSON value; undefined means absent.
  value: unknown;
  // onChange persists the next raw JSON value (undefined clears the key).
  onChange: (value: unknown) => void;
}

// DictFieldInput renders the control matching spec.kind. Durations stay
// textual ("5s", "1m") because the edge parses Go duration strings; integer
// fields emit JSON numbers and clear back to undefined.
export function DictFieldInput({ spec, value, onChange }: DictFieldInputProps) {
  const header = <FieldLabel label={spec.label} required={spec.required} hint={spec.hint} />;

  switch (spec.kind) {
    case "integer":
      // An options-backed integer field uses a Select (e.g. Modbus DataBits)
      // while still emitting a JSON number.
      if (spec.options) {
        return (
          <div>
            {header}
            <Select
              size="small"
              style={{ width: "100%" }}
              allowClear
              value={value === undefined || value === null ? undefined : Number(value)}
              placeholder={spec.placeholder}
              options={spec.options.map((option) => ({
                value: Number(option.value),
                label: option.label,
              }))}
              onChange={(next) => onChange(next ?? undefined)}
            />
          </div>
        );
      }
      return (
        <div>
          {header}
          <InputNumber
            size="small"
            style={{ width: "100%" }}
            value={typeof value === "number" ? value : undefined}
            placeholder={spec.placeholder}
            min={spec.min}
            max={spec.max}
            precision={0}
            onChange={(next) => onChange(next ?? undefined)}
          />
        </div>
      );
    case "number":
      return (
        <div>
          {header}
          <InputNumber
            size="small"
            style={{ width: "100%" }}
            value={typeof value === "number" ? value : undefined}
            placeholder={spec.placeholder}
            min={spec.min}
            max={spec.max}
            onChange={(next) => onChange(next ?? undefined)}
          />
        </div>
      );
    case "boolean":
      return (
        <div style={{ display: "flex", alignItems: "center", gap: 8, height: 24 }}>
          <Switch
            size="small"
            checked={value === true}
            onChange={(checked) => onChange(checked)}
          />
          <FieldLabel label={spec.label} required={spec.required} inline hint={spec.hint} />
        </div>
      );
    case "select":
      return (
        <div>
          {header}
          <Select
            size="small"
            style={{ width: "100%" }}
            allowClear
            showSearch
            value={value === undefined || value === null ? undefined : String(value)}
            placeholder={spec.placeholder}
            options={(spec.options ?? []).map((option) => ({
              value: option.value,
              label: option.label,
            }))}
            onChange={(next) => onChange(next ?? undefined)}
          />
        </div>
      );
    case "duration":
    case "string":
    default:
      return (
        <div>
          {header}
          <Input
            size="small"
            value={value === undefined || value === null ? "" : String(value)}
            placeholder={spec.placeholder}
            onChange={(event) =>
              onChange(event.target.value === "" ? undefined : event.target.value)
            }
          />
        </div>
      );
  }
}

// FieldLabel renders the small label above/beside a control.
function FieldLabel({
  label,
  required,
  inline,
  hint,
}: {
  label: string;
  required?: boolean;
  inline?: boolean;
  hint?: string;
}) {
  return (
    <span
      style={{
        fontSize: 12,
        color: "rgba(0,0,0,0.65)",
        display: inline ? undefined : "flex",
        alignItems: "center",
        gap: 4,
        marginBottom: inline ? 0 : 2,
      }}
    >
      {required ? <span style={{ color: "#ff4d4f" }}>* </span> : null}
      {label}
      {hint && (
        <Tooltip title={hint}>
          <QuestionCircleOutlined style={{ color: "rgba(0,0,0,0.35)" }} />
        </Tooltip>
      )}
    </span>
  );
}
