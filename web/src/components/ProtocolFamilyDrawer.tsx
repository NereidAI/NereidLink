// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProtocolFamilyDrawer creates and edits one global device-service catalog
// family (ADR-0033). Basic identity (family key, display name, service,
// protocol key, doc URL), the transport list and the structured connection /
// point field descriptors are all editable. Builtin families keep their key
// fixed and are saved with PUT; custom families are POSTed. Field keys are
// emitted verbatim into the edge JSON, so casing is the operator's
// responsibility and shown verbatim.

import {
  App as AntApp,
  Button,
  Drawer,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Table,
  Typography,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  createProtocolFamily,
  updateProtocolFamily,
  type FieldKind,
  type FieldSpec,
  type ProtocolFamily,
  type Transport,
} from "../api/protocolCatalog";
import { extractError } from "../api/client";
import { protocolCatalogQueryKey } from "./connector/useProtocolCatalog";

const FIELD_KINDS: FieldKind[] = [
  "string",
  "integer",
  "number",
  "boolean",
  "duration",
  "select",
];

interface FamilyFormValues {
  family: string;
  display_name: string;
  service_name: string;
  protocol_key: string;
  doc_url: string;
}

interface ProtocolFamilyDrawerProps {
  open: boolean;
  // family is undefined when creating a new custom entry.
  family?: ProtocolFamily;
  onClose: () => void;
}

// ProtocolFamilyDrawer renders the create/edit drawer.
export default function ProtocolFamilyDrawer({
  open,
  family,
  onClose,
}: ProtocolFamilyDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FamilyFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const builtin = family?.builtin ?? false;
  const [transports, setTransports] = useState<Transport[]>(family?.transports ?? []);
  const [connectionFields, setConnectionFields] = useState<FieldSpec[]>(
    family?.connection_fields ?? [],
  );
  const [pointFields, setPointFields] = useState<FieldSpec[]>(family?.point_fields ?? []);
  // defaultAddressText holds the raw JSON text of the address template so
  // operators can edit it directly and so PUT never wipes the stored value.
  const [defaultAddressText, setDefaultAddressText] = useState<string>(
    family?.default_point_address && Object.keys(family.default_point_address).length > 0
      ? JSON.stringify(family.default_point_address, null, 2)
      : "",
  );
  const [addressError, setAddressError] = useState<string>("");

  const mutation = useMutation({
    mutationFn: async (values: FamilyFormValues) => {
      const addressText = defaultAddressText.trim();
      let defaultAddress: Record<string, unknown> = {};
      if (addressText) {
        let parsed: unknown;
        try {
          parsed = JSON.parse(addressText);
        } catch {
          setAddressError(t("pages.deviceServices.form.addressJsonInvalid"));
          throw new Error("__INVALID_ADDRESS__");
        }
        if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
          setAddressError(t("pages.deviceServices.form.addressJsonObject"));
          throw new Error("__INVALID_ADDRESS__");
        }
        defaultAddress = parsed as Record<string, unknown>;
      }
      const payload = {
        family: values.family.trim(),
        display_name: values.display_name.trim(),
        service_name: values.service_name.trim(),
        protocol_key: values.protocol_key.trim(),
        doc_url: values.doc_url.trim(),
        transports,
        connection_fields: connectionFields,
        point_fields: pointFields,
        default_point_address: defaultAddress,
      };
      if (family) {
        return updateProtocolFamily(family.family, payload);
      }
      return createProtocolFamily(payload);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: protocolCatalogQueryKey });
      message.success(t("pages.deviceServices.toasts.saved"));
      onClose();
    },
    onError: (error) => {
      if (error instanceof Error && error.message === "__INVALID_ADDRESS__") {
        message.error(t("pages.deviceServices.form.addressFixBeforeSave"));
        return;
      }
      message.error(extractError(error).message);
    },
  });

  const title = family
    ? t("pages.deviceServices.drawer.editTitle", { name: family.display_name })
    : t("pages.deviceServices.drawer.createTitle");

  return (
    <Drawer
      open={open}
      title={title}
      width={760}
      onClose={onClose}
      destroyOnHidden
      extra={
        <Space>
          <Button onClick={onClose}>{t("common.cancel")}</Button>
          <Button
            type="primary"
            loading={mutation.isPending}
            onClick={() => form.submit()}
          >
            {t("common.save")}
          </Button>
        </Space>
      }
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        // The drawer is remounted per target (key on the parent), so
        // initialValues reliably populate on every create/edit open.
        initialValues={{
          family: family?.family ?? "",
          display_name: family?.display_name ?? "",
          service_name: family?.service_name ?? "",
          protocol_key: family?.protocol_key ?? "",
          doc_url: family?.doc_url ?? "",
        }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("pages.deviceServices.form.family")}
          name="family"
          rules={[{ required: true, message: t("pages.deviceServices.form.familyRequired") }]}
          extra={t("pages.deviceServices.form.familyHint")}
        >
          <Input placeholder="s7" disabled={builtin} />
        </Form.Item>
        <Form.Item
          label={t("pages.deviceServices.form.displayName")}
          name="display_name"
          rules={[{ required: true, message: t("pages.deviceServices.form.displayNameRequired") }]}
        >
          <Input placeholder="Siemens S7" />
        </Form.Item>
        <Space wrap align="start" style={{ display: "flex" }}>
          <Form.Item
            style={{ width: 240 }}
            label={t("pages.deviceServices.form.serviceName")}
            name="service_name"
          >
            <Input placeholder="device-s7" />
          </Form.Item>
          <Form.Item
            style={{ width: 200 }}
            label={t("pages.deviceServices.form.protocolKey")}
            name="protocol_key"
          >
            <Input placeholder="s7" />
          </Form.Item>
          <Form.Item style={{ flex: 1, minWidth: 240 }} label={t("pages.deviceServices.form.docUrl")} name="doc_url">
            <Input placeholder="https://docs.edgexfoundry.org/..." />
          </Form.Item>
        </Space>
      </Form>

      <SectionTitle>{t("pages.deviceServices.form.transports")}</SectionTitle>
      <TransportEditor values={transports} onChange={setTransports} />

      <SectionTitle>{t("pages.deviceServices.form.connectionFields")}</SectionTitle>
      <FieldEditor values={connectionFields} onChange={setConnectionFields} connection />

      <SectionTitle>{t("pages.deviceServices.form.pointFields")}</SectionTitle>
      <FieldEditor values={pointFields} onChange={setPointFields} />

      <SectionTitle>{t("pages.deviceServices.form.defaultAddress")}</SectionTitle>
      <Input.TextArea
        rows={4}
        spellCheck={false}
        value={defaultAddressText}
        status={addressError ? "error" : undefined}
        placeholder={'{\n  "NodeName": "DB4.DBX0.0"\n}'}
        onChange={(e) => {
          setDefaultAddressText(e.target.value);
          setAddressError("");
        }}
        onBlur={() => {
          const text = defaultAddressText.trim();
          if (!text) {
            setAddressError("");
            return;
          }
          try {
            const parsed = JSON.parse(text) as unknown;
            if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
              setAddressError(t("pages.deviceServices.form.addressJsonObject"));
              return;
            }
          } catch {
            setAddressError(t("pages.deviceServices.form.addressJsonInvalid"));
          }
        }}
      />
      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
        {addressError || t("pages.deviceServices.form.defaultAddressHint")}
      </Typography.Text>
    </Drawer>
  );
}

// SectionTitle is a small uppercase group label.
function SectionTitle({ children }: { children: React.ReactNode }) {
  return (
    <Typography.Text strong style={{ display: "block", margin: "16px 0 8px" }}>
      {children}
    </Typography.Text>
  );
}

// FieldAdvanced edits select options and numeric bounds in the expanded row.
function FieldAdvanced({
  row,
  onChange,
}: {
  row: FieldSpec;
  onChange: (patch: Partial<FieldSpec>) => void;
}) {
  const { t } = useTranslation();
  const options = row.options ?? [];
  const updateOption = (i: number, patch: Partial<{ value: string; label: string }>) => {
    const next = options.map((o, x) => (x === i ? { ...o, ...patch } : o));
    onChange({ options: next });
  };
  return (
    <Space direction="vertical" size={6} style={{ display: "flex" }}>
      {row.kind === "select" && (
        <>
          {options.map((opt, i) => (
            <Space.Compact key={i}>
              <Input
                size="small"
                style={{ width: 160 }}
                addonBefore={t("pages.deviceServices.form.key")}
                value={opt.value}
                onChange={(e) => updateOption(i, { value: e.target.value })}
              />
              <Input
                size="small"
                addonBefore={t("pages.deviceServices.form.label")}
                value={opt.label}
                onChange={(e) => updateOption(i, { label: e.target.value })}
              />
              <Button
                size="small"
                danger
                icon={<DeleteOutlined />}
                onClick={() => onChange({ options: options.filter((_, x) => x !== i) })}
              />
            </Space.Compact>
          ))}
          <Button
            size="small"
            type="dashed"
            icon={<PlusOutlined />}
            onClick={() => onChange({ options: [...options, { value: "", label: "" }] })}
          >
            {t("pages.deviceServices.form.addOption")}
          </Button>
        </>
      )}
      {(row.kind === "integer" || row.kind === "number") && (
        <Space>
          <span>{t("pages.deviceServices.form.min")}</span>
          <InputNumber
            size="small"
            value={row.min}
            onChange={(v) => onChange({ min: v ?? undefined })}
          />
          <span>{t("pages.deviceServices.form.max")}</span>
          <InputNumber
            size="small"
            value={row.max}
            onChange={(v) => onChange({ max: v ?? undefined })}
          />
        </Space>
      )}
    </Space>
  );
}

function TransportEditor({
  values,
  onChange,
}: {
  values: Transport[];
  onChange: (next: Transport[]) => void;
}) {
  const { t } = useTranslation();
  const update = (i: number, patch: Partial<Transport>) => {
    const next = [...values];
    next[i] = { ...next[i], ...patch };
    onChange(next);
  };
  return (
    <Space direction="vertical" size={6} style={{ display: "flex" }}>
      {values.map((tr, i) => (
        <Space.Compact key={i} block>
          <Input
            style={{ width: 200 }}
            addonBefore={t("pages.deviceServices.form.key")}
            value={tr.value}
            onChange={(e) => update(i, { value: e.target.value })}
          />
          <Input
            addonBefore={t("pages.deviceServices.form.label")}
            value={tr.label}
            onChange={(e) => update(i, { label: e.target.value })}
          />
          <Button danger icon={<DeleteOutlined />} onClick={() => onChange(values.filter((_, x) => x !== i))} />
        </Space.Compact>
      ))}
      <Button
        type="dashed"
        size="small"
        icon={<PlusOutlined />}
        onClick={() => onChange([...values, { value: "", label: "" }])}
      >
        {t("pages.deviceServices.form.addTransport")}
      </Button>
    </Space>
  );
}

// FieldEditor edits the structured connection/point field descriptors in a
// compact expandable table.
function FieldEditor({
  values,
  onChange,
  connection = false,
}: {
  values: FieldSpec[];
  onChange: (next: FieldSpec[]) => void;
  connection?: boolean;
}) {
  const { t } = useTranslation();
  const update = (i: number, patch: Partial<FieldSpec>) => {
    const next = [...values];
    next[i] = { ...next[i], ...patch };
    onChange(next);
  };
  const columns = [
    {
      title: t("pages.deviceServices.form.key"),
      dataIndex: "key",
      width: 160,
      render: (_: unknown, _row: FieldSpec, i: number) => (
        <Input
          size="small"
          value={values[i].key}
          onChange={(e) => update(i, { key: e.target.value })}
        />
      ),
    },
    {
      title: t("pages.deviceServices.form.label"),
      dataIndex: "label",
      width: 170,
      render: (_: unknown, _row: FieldSpec, i: number) => (
        <Input
          size="small"
          value={values[i].label}
          onChange={(e) => update(i, { label: e.target.value })}
        />
      ),
    },
    {
      title: t("pages.deviceServices.form.kind"),
      dataIndex: "kind",
      width: 120,
      render: (_: unknown, _row: FieldSpec, i: number) => (
        <Select
          size="small"
          style={{ width: 112 }}
          value={values[i].kind || "string"}
          options={FIELD_KINDS.map((kind) => ({ value: kind, label: kind }))}
          onChange={(kind) => update(i, { kind })}
        />
      ),
    },
    {
      title: t("pages.deviceServices.form.required"),
      dataIndex: "required",
      width: 70,
      render: (_: unknown, _row: FieldSpec, i: number) => (
        <Switch size="small" checked={!!values[i].required} onChange={(v) => update(i, { required: v })} />
      ),
    },
    {
      title: t("pages.deviceServices.form.placeholder"),
      dataIndex: "placeholder",
      render: (_: unknown, _row: FieldSpec, i: number) => (
        <Input
          size="small"
          value={values[i].placeholder ?? ""}
          onChange={(e) => update(i, { placeholder: e.target.value })}
        />
      ),
    },
    {
      title: "",
      width: 40,
      render: (_: unknown, _row: FieldSpec, i: number) => (
        <Button
          type="text"
          danger
          size="small"
          icon={<DeleteOutlined />}
          onClick={() => onChange(values.filter((_, x) => x !== i))}
        />
      ),
    },
  ];

  return (
    <>
      <Table
        size="small"
        rowKey={(_, i) => String(i)}
        columns={columns}
        dataSource={values}
        pagination={false}
        expandable={{
          rowExpandable: (row) => row.kind === "select" || row.kind === "integer" || row.kind === "number",
          expandedRowRender: (_row, i) => (
            <FieldAdvanced row={values[i]} onChange={(patch) => update(i, patch)} />
          ),
        }}
      />
      <Button
        type="dashed"
        size="small"
        style={{ marginTop: 8 }}
        icon={<PlusOutlined />}
        onClick={() =>
          onChange([
            ...values,
            connection
              ? ({ key: "", label: "", kind: "string", transports: ["*"] } as FieldSpec)
              : ({ key: "", label: "", kind: "string" } as FieldSpec),
          ])
        }
      >
        {connection
          ? t("pages.deviceServices.form.addConnectionField")
          : t("pages.deviceServices.form.addPointField")}
      </Button>
    </>
  );
}
