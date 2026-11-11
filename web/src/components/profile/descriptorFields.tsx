// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// descriptorFields holds the descriptor editing primitives shared by every
// profile editing surface (the detail page's point section, and historically
// the single create/edit modal). A profile is a vendor-neutral point library
// (ADR-0032): each descriptor carries calibration (unit/scale/offset/...), an
// optional transform, vendor-owned spec columns (data_type/read_write/...),
// and a connector `address` object. The address is edited outside the antd
// form store (rc-field-form cannot reliably hold the parallel JSON objects),
// so the card is presentational and the parent owns the address array.

import {
  Button,
  Card,
  Col,
  Divider,
  Form,
  Input,
  InputNumber,
  Row,
  Select,
  Space,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, MinusCircleOutlined, PlusOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useState } from "react";
import type { FormInstance } from "antd";

import { DictFieldInput } from "../connector/DictFields";
import type { CatalogDictionary } from "../connector/useProtocolCatalog";
import {
  neutralTypeOptions,
  readWriteOptions,
  transformOptions,
} from "./descriptorModel";

// DescriptorCardProps configures one descriptor card.
interface DescriptorCardProps {
  // fieldName is the row index inside the descriptors Form.List.
  fieldName: number;
  // form is the parent form that owns the descriptors Form.List.
  form: FormInstance;
  // remove deletes the row both from the list and the address array.
  remove: (index: number) => void;
  // specEditable enables the ADR-0032 point-spec fields (manual profiles).
  specEditable: boolean;
  // protocolDict, when present, renders structured point-address fields.
  protocolDict: CatalogDictionary | null;
  // address is the point addressing object held outside the form store.
  address: Record<string, unknown>;
  // onAddressChange persists a new addressing object.
  onAddressChange: (address: Record<string, unknown>) => void;
}

// DescriptorCard renders one descriptor with type-specific fields.
export function DescriptorCard({
  fieldName,
  form,
  remove,
  specEditable,
  protocolDict,
  address,
  onAddressChange,
}: DescriptorCardProps) {
  const { t } = useTranslation();
  const transform: string | undefined = Form.useWatch(
    ["descriptors", fieldName, "transform_type"],
    form,
  );
  const inactive: boolean | undefined = Form.useWatch(
    ["descriptors", fieldName, "inactive"],
    form,
  );

  const transformLabel: Record<string, string> = {
    identity: t("enums.identity"),
    linear: t("enums.linear"),
    table: t("enums.table"),
    expression: t("enums.expression"),
    enum: t("enums.enum"),
  };

  return (
    <Card
      size="small"
      style={{ marginBottom: 12, opacity: inactive ? 0.6 : 1 }}
      title={
        <Space>
          <Form.Item
            name={[fieldName, "measurement_key"]}
            style={{ marginBottom: 0 }}
            rules={[{ required: true, message: t("pages.profiles.form.keyRequired") }]}
          >
            <Input placeholder={t("pages.profiles.form.keyPlaceholder")} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item
            name={[fieldName, "transform_type"]}
            style={{ marginBottom: 0 }}
          >
            <Select
              style={{ width: 130 }}
              options={transformOptions.map((option) => ({
                value: option.value,
                label: transformLabel[option.value] ?? option.label,
              }))}
            />
          </Form.Item>
          <Form.Item name={[fieldName, "id"]} hidden>
            <Input />
          </Form.Item>
          {inactive && <Tag color="default">{t("pages.profiles.form.inactive")}</Tag>}
        </Space>
      }
      extra={
        <Button
          type="text"
          danger
          icon={<MinusCircleOutlined />}
          onClick={() => remove(fieldName)}
        />
      }
    >
      <Space wrap align="start">
        <Form.Item label={t("pages.profiles.form.unit")} name={[fieldName, "unit"]}>
          <Input style={{ width: 90 }} />
        </Form.Item>
        <Form.Item label={t("pages.profiles.form.scale")} name={[fieldName, "scale"]}>
          <InputNumber style={{ width: 110 }} />
        </Form.Item>
        <Form.Item label={t("pages.profiles.form.offset")} name={[fieldName, "offset"]}>
          <InputNumber style={{ width: 110 }} />
        </Form.Item>
        <Form.Item label={t("pages.profiles.form.precision")} name={[fieldName, "precision"]}>
          <InputNumber style={{ width: 100 }} />
        </Form.Item>
        <Form.Item
          label={t("pages.profiles.form.expectedPeriod")}
          name={[fieldName, "expected_period_seconds"]}
          tooltip={t("pages.profiles.form.expectedPeriodHint")}
        >
          <InputNumber min={1} precision={0} style={{ width: 130 }} placeholder="s" />
        </Form.Item>
        <Form.Item
          label={t("pages.profiles.form.toleranceMultiplier")}
          name={[fieldName, "tolerance_multiplier"]}
          tooltip={t("pages.profiles.form.toleranceMultiplierHint")}
        >
          <InputNumber min={1} step={0.5} style={{ width: 110 }} placeholder="3" />
        </Form.Item>
      </Space>

      {transform === "expression" && (
        <Form.Item
          label={t("pages.profiles.form.expression")}
          name={[fieldName, "expression"]}
          rules={[{ required: true, message: t("pages.profiles.form.expressionRequired") }]}
        >
          <Input.TextArea rows={2} />
        </Form.Item>
      )}

      {transform === "table" && (
        <Form.List name={[fieldName, "breakpoints"]}>
          {(rows, { add, remove }) => (
            <>
              {rows.map((row) => (
                <Space key={row.key} style={{ display: "flex", marginBottom: 8 }}>
                  <Form.Item name={[row.name, "x"]} noStyle>
                    <InputNumber placeholder={t("pages.profiles.form.x")} style={{ width: 120 }} />
                  </Form.Item>
                  <Form.Item name={[row.name, "y"]} noStyle>
                    <InputNumber placeholder={t("pages.profiles.form.y")} style={{ width: 120 }} />
                  </Form.Item>
                  <DeleteOutlined onClick={() => remove(row.name)} />
                </Space>
              ))}
              <Button type="dashed" onClick={() => add({})} icon={<PlusOutlined />}>
                {t("pages.profiles.form.addBreakpoint")}
              </Button>
            </>
          )}
        </Form.List>
      )}

      {transform === "enum" && (
        <Form.List name={[fieldName, "enum_pairs"]}>
          {(rows, { add, remove }) => (
            <>
              {rows.map((row) => (
                <Space key={row.key} style={{ display: "flex", marginBottom: 8 }}>
                  <Form.Item name={[row.name, "k"]} noStyle>
                    <Input placeholder={t("pages.profiles.form.rawKey")} style={{ width: 140 }} />
                  </Form.Item>
                  <Form.Item name={[row.name, "v"]} noStyle>
                    <Input placeholder={t("pages.profiles.form.enumLabel")} style={{ width: 140 }} />
                  </Form.Item>
                  <DeleteOutlined onClick={() => remove(row.name)} />
                </Space>
              ))}
              <Button type="dashed" onClick={() => add({})} icon={<PlusOutlined />}>
                {t("pages.profiles.form.addEnum")}
              </Button>
            </>
          )}
        </Form.List>
      )}

      {specEditable && (
        <>
          <Divider style={{ margin: "8px 0" }} orientation="left" plain>
            {t("pages.profiles.form.specSection")}
          </Divider>
          <Space wrap align="start">
            <Form.Item
              label={t("pages.profiles.form.dataType")}
              name={[fieldName, "data_type"]}
            >
              <Select
                allowClear
                style={{ width: 120 }}
                placeholder={t("pages.profiles.form.dataTypePlaceholder")}
                options={neutralTypeOptions.map((value) => ({ value, label: value }))}
              />
            </Form.Item>
            <Form.Item
              label={t("pages.profiles.form.readWrite")}
              name={[fieldName, "read_write"]}
            >
              <Select
                allowClear
                style={{ width: 110 }}
                options={readWriteOptions.map((value) => ({ value, label: value }))}
              />
            </Form.Item>
            <Form.Item
              label={t("pages.profiles.form.edgeDefault")}
              name={[fieldName, "edge_default"]}
            >
              <Input style={{ width: 140 }} />
            </Form.Item>
            <Form.Item label={t("pages.profiles.form.edgeMin")} name={[fieldName, "edge_min"]}>
              <InputNumber style={{ width: 120 }} />
            </Form.Item>
            <Form.Item label={t("pages.profiles.form.edgeMax")} name={[fieldName, "edge_max"]}>
              <InputNumber style={{ width: 120 }} />
            </Form.Item>
          </Space>
        </>
      )}

      {/* Connector address is operator-maintainable on every source: an
          edge/import frame frequently omits the concrete point binding. A
          known family with structured point fields uses those; every other
          family (and families like onvif/mqtt with free-form point attrs)
          falls back to raw JSON. */}
      {protocolDict && protocolDict.pointFields.length > 0 ? (
        <PointAddressFields
          address={address}
          onAddressChange={onAddressChange}
          protocolDict={protocolDict}
        />
      ) : (
        <FreeAddressField address={address} onAddressChange={onAddressChange} />
      )}
    </Card>
  );
}

// PointAddressFields renders the protocol-dictionary point-address fields
// (e.g. Modbus primaryTable/startingAddress) backed by the address object
// held outside the form store. Any non-dictionary extra keys stay visible as
// raw JSON text so switching protocol families never silently drops data.
function PointAddressFields({
  address,
  onAddressChange,
  protocolDict,
}: {
  address: Record<string, unknown>;
  onAddressChange: (address: Record<string, unknown>) => void;
  protocolDict: CatalogDictionary;
}) {
  const { t } = useTranslation();
  const known = new Set(protocolDict.pointFields.map((field) => field.key));
  const extraEntries = Object.entries(address).filter(([key]) => !known.has(key));

  const setField = (key: string, value: unknown) => {
    const next = { ...address };
    if (value === undefined) delete next[key];
    else next[key] = value;
    onAddressChange(next);
  };

  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t("pages.profiles.form.address")}
      </Typography.Text>
      <Row gutter={[8, 4]} style={{ marginTop: 2 }}>
        {protocolDict.pointFields.map((field) => (
          <Col key={field.key} span={6}>
            <DictFieldInput
              spec={field}
              value={address[field.key]}
              onChange={(value) => setField(field.key, value)}
            />
          </Col>
        ))}
      </Row>
      {extraEntries.length > 0 && (
        <ExtraAddressEditor
          address={address}
          known={known}
          onChange={onAddressChange}
        />
      )}
    </div>
  );
}

// ExtraAddressEditor edits the non-dictionary keys of a point address as raw
// JSON; dictionary-managed keys stay in the structured fields and are merged
// back verbatim on save, so switching protocol families cannot drop data.
function ExtraAddressEditor({
  address,
  known,
  onChange,
}: {
  address: Record<string, unknown>;
  known: Set<string>;
  onChange: (address: Record<string, unknown>) => void;
}) {
  const { t } = useTranslation();
  const extra = Object.fromEntries(Object.entries(address).filter(([k]) => !known.has(k)));
  const [text, setText] = useState(() => JSON.stringify(extra, null, 2));
  const [error, setError] = useState(false);
  const commit = () => {
    if (!text.trim()) {
      setError(false);
      const next = { ...address };
      for (const key of Object.keys(next)) if (!known.has(key)) delete next[key];
      onChange(next);
      return;
    }
    try {
      const parsed = JSON.parse(text) as unknown;
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        setError(true);
        return;
      }
      const next: Record<string, unknown> = {};
      for (const [k, v] of Object.entries(address)) if (known.has(k)) next[k] = v;
      Object.assign(next, parsed);
      setError(false);
      onChange(next);
    } catch {
      setError(true);
    }
  };
  return (
    <div style={{ marginTop: 8 }}>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t("pages.profiles.form.addressExtra")}
      </Typography.Text>
      <Input.TextArea
        rows={2}
        spellCheck={false}
        value={text}
        status={error ? "error" : undefined}
        style={{ fontFamily: "monospace", fontSize: 12 }}
        onChange={(event) => setText(event.target.value)}
        onBlur={commit}
      />
      {error && (
        <Typography.Text type="danger" style={{ fontSize: 12 }}>
          {t("pages.profiles.form.addressJsonInvalid")}
        </Typography.Text>
      )}
    </div>
  );
}

// FreeAddressField edits the opaque point address object as raw JSON when no
// protocol dictionary exists for the profile's family.
function FreeAddressField({
  address,
  onAddressChange,
}: {
  address: Record<string, unknown>;
  onAddressChange: (address: Record<string, unknown>) => void;
}) {
  const { t } = useTranslation();
  const [text, setText] = useState(() =>
    Object.keys(address).length ? JSON.stringify(address, null, 2) : "",
  );
  const [error, setError] = useState(false);
  const commit = () => {
    if (!text.trim()) {
      setError(false);
      onAddressChange({});
      return;
    }
    try {
      const parsed = JSON.parse(text) as unknown;
      if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
        setError(true);
        return;
      }
      setError(false);
      onAddressChange(parsed as Record<string, unknown>);
    } catch {
      setError(true);
    }
  };
  return (
    <div>
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {t("pages.profiles.form.address")}
      </Typography.Text>
      <Input.TextArea
        rows={2}
        spellCheck={false}
        placeholder='{ "nodeId": "ns=2;s=Temperature" }'
        value={text}
        status={error ? "error" : undefined}
        style={{ fontFamily: "monospace", fontSize: 12 }}
        onChange={(event) => setText(event.target.value)}
        onBlur={commit}
      />
      {error && (
        <Typography.Text type="danger" style={{ fontSize: 12 }}>
          {t("pages.profiles.form.addressJsonInvalid")}
        </Typography.Text>
      )}
    </div>
  );
}
