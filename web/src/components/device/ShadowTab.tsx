// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ShadowTab displays desired/reported state and writes desired patches.

import { useMemo, useRef, useState } from "react";
import {
  App as AntApp,
  Button,
  Card,
  Divider,
  Drawer,
  Empty,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  getDesiredSchema,
  updateDesired,
  type DesiredKeyField,
} from "../../api/shadow";
import { extractError } from "../../api/client";
import FilterSearchBar from "../../components/FilterSearchBar";
import { useDeviceShadow } from "./useDeviceShadow";

interface ShadowTabProps {
  deviceId: string;
}

interface ShadowRow {
  key: string;
  desired?: unknown;
  reported?: unknown;
  inSync: boolean;
}

// FieldValueKind enumerates the typed editors a common key supports.
type FieldValueKind = "boolean" | "number" | "string" | "object" | "array";

// FieldDraft is one typed row in the desired editor.
interface FieldDraft {
  id: number;
  key: string;
  kind: FieldValueKind;
  // Scalars hold a typed value; object/array hold raw JSON text.
  value: boolean | number | string;
  // catalog marks a profile-derived common key: its key is locked and its
  // number input is bounded by min/max; unit is shown as a suffix.
  catalog?: boolean;
  min?: number | null;
  max?: number | null;
  unit?: string;
}

const SCALAR_KINDS: FieldValueKind[] = ["boolean", "number", "string", "object", "array"];

// inferKind maps an existing shadow JSON value to an editor kind.
function inferKind(value: unknown): FieldValueKind {
  if (Array.isArray(value)) return "array";
  if (value !== null && typeof value === "object") return "object";
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "number") return "number";
  return "string";
}

// initialDrafts seeds the form from existing desired keys so common keys are
// editable without a curated catalog.
function initialDrafts(desired: Record<string, unknown>): FieldDraft[] {
  return Object.entries(desired).map(([key, value], index) => {
    const kind = inferKind(value);
    return {
      id: index,
      key,
      kind,
      value:
        kind === "object" || kind === "array"
          ? JSON.stringify(value, null, 2)
          : (value as boolean | number | string),
    };
  });
}

// catalogDefault resolves the initial value of a profile-derived key.
function catalogDefault(field: DesiredKeyField): boolean | number | string {
  if (field.default !== undefined && field.default !== null) {
    if (field.type === "number" && typeof field.default === "number") {
      return field.default;
    }
    if (field.type === "boolean" && typeof field.default === "boolean") {
      return field.default;
    }
    if (field.type === "string") return String(field.default);
  }
  if (field.type === "boolean") return false;
  if (field.type === "number") return field.min ?? 0;
  return "";
}

// draftFromCatalog builds one locked-key row from a profile descriptor.
function draftFromCatalog(field: DesiredKeyField, id: number): FieldDraft {
  return {
    id,
    key: field.key,
    kind: field.type,
    value: catalogDefault(field),
    catalog: true,
    min: field.min,
    max: field.max,
    unit: field.unit,
  };
}

export default function ShadowTab({ deviceId }: ShadowTabProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [open, setOpen] = useState(false);
  const [fields, setFields] = useState<FieldDraft[]>([]);
  const [extra, setExtra] = useState("{}");
  const [fieldSeq, setFieldSeq] = useState(0);
  const [pickKey, setPickKey] = useState<string>();
  const [keyword, setKeyword] = useState("");

  const query = useDeviceShadow(deviceId, "device-shadow-editor");
  const shadow = query.data;

  // Profile-derived catalog of writable keys for the editor. It is fetched
  // once for the device; a device without a profile yields an empty list.
  const schemaQuery = useQuery({
    queryKey: ["device-shadow-schema", deviceId],
    queryFn: () => getDesiredSchema(deviceId),
  });
  const catalogFields = schemaQuery.data?.fields ?? [];

  // baselineKeys records the desired keys present when the editor opened, so
  // a removed row can be sent as a JSON null (top-level key deletion).
  const baselineKeysRef = useRef<Set<string>>(new Set());

  const openEditor = () => {
    const byKey = new Map(catalogFields.map((f) => [f.key, f]));
    const drafts = initialDrafts(shadow?.desired ?? {}).map((draft) => {
      const meta = byKey.get(draft.key);
      // Annotate an existing desired key that is also a profile-writable key
      // so its editor keeps the catalog bounds/unit, but leave its current
      // value and inferred kind untouched.
      return meta ? { ...draft, catalog: true, min: meta.min, max: meta.max, unit: meta.unit } : draft;
    });
    baselineKeysRef.current = new Set(Object.keys(shadow?.desired ?? {}));
    setFields(drafts);
    setFieldSeq(drafts.length);
    setPickKey(undefined);
    setExtra("{}");
    setOpen(true);
  };

  const addCatalogField = (key: string) => {
    const meta = catalogFields.find((f) => f.key === key);
    if (!meta) return;
    setFields((prev) => {
      if (prev.some((f) => f.key === key)) return prev;
      return [...prev, draftFromCatalog(meta, fieldSeq)];
    });
    setFieldSeq((n) => n + 1);
  };

  const mutation = useMutation({
    mutationFn: async () => {
      const patch: Record<string, unknown> = {};
      const seen = new Set<string>();
      for (const field of fields) {
        const key = field.key.trim();
        if (!key) throw new Error(t("pages.deviceDetail.shadow.keyRequired"));
        if (seen.has(key)) throw new Error(t("pages.deviceDetail.shadow.duplicateKey"));
        seen.add(key);
        if (field.kind === "object" || field.kind === "array") {
          let parsed: unknown;
          try {
            parsed = JSON.parse(field.value as string);
          } catch {
            throw new Error(t("pages.deviceDetail.shadow.invalidJson"));
          }
          const isObject = parsed !== null && typeof parsed === "object";
          if (field.kind === "object" && (!isObject || Array.isArray(parsed))) {
            throw new Error(t("pages.deviceDetail.shadow.jsonObjectRequired"));
          }
          if (field.kind === "array" && !Array.isArray(parsed)) {
            throw new Error(t("pages.deviceDetail.shadow.invalidJson"));
          }
          patch[key] = parsed;
        } else {
          patch[key] = field.value;
        }
      }
      // Removed rows delete the previously-present top-level key.
      for (const key of baselineKeysRef.current) {
        if (!seen.has(key)) patch[key] = null;
      }
      let extraParsed: Record<string, unknown> = {};
      if (extra.trim() !== "" && extra.trim() !== "{}") {
        let raw: unknown;
        try {
          raw = JSON.parse(extra);
        } catch {
          throw new Error(t("pages.deviceDetail.shadow.invalidJson"));
        }
        if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
          throw new Error(t("pages.deviceDetail.shadow.jsonObjectRequired"));
        }
        extraParsed = raw as Record<string, unknown>;
      }
      for (const [key, value] of Object.entries(extraParsed)) {
        if (seen.has(key)) throw new Error(t("pages.deviceDetail.shadow.duplicateKey"));
        patch[key] = value;
      }
      return updateDesired(deviceId, patch, shadow?.etag ?? '"0"');
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({
        queryKey: ["device-shadow", deviceId],
      });
      message.success(t("pages.deviceDetail.shadow.saved"));
      setOpen(false);
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const addField = () => {
    setFields((prev) => [
      ...prev,
      { id: fieldSeq, key: "", kind: "string", value: "" },
    ]);
    setFieldSeq((n) => n + 1);
  };

  const updateField = (id: number, patch: Partial<FieldDraft>) => {
    setFields((prev) =>
      prev.map((field) =>
        field.id === id
          ? // Changing kind resets the value to a valid default.
            patch.kind && patch.kind !== field.kind
              ? {
                  ...field,
                  ...patch,
                  value:
                    patch.kind === "boolean"
                      ? true
                      : patch.kind === "number"
                        ? 0
                        : patch.kind === "string"
                          ? ""
                          : patch.kind === "array"
                            ? "[]"
                            : "{}",
                }
              : { ...field, ...patch }
          : field,
      ),
    );
  };

  const removeField = (id: number) => {
    setFields((prev) => prev.filter((field) => field.id !== id));
  };

  // Memoized so the filtered-rows memo keeps a stable dependency identity
  // instead of rebuilding the derived row array on every render.
  const rows: ShadowRow[] = useMemo(
    () =>
      shadow
        ? Array.from(
            new Set([
              ...Object.keys(shadow.desired),
              ...Object.keys(shadow.reported),
            ]),
          ).map((key) => {
            const hasDesired = Object.prototype.hasOwnProperty.call(shadow.desired, key);
            const hasReported = Object.prototype.hasOwnProperty.call(shadow.reported, key);
            return {
              key,
              desired: hasDesired ? shadow.desired[key] : undefined,
              reported: hasReported ? shadow.reported[key] : undefined,
              inSync: hasDesired && hasReported &&
                JSON.stringify(shadow.desired[key]) === JSON.stringify(shadow.reported[key]),
            };
          })
        : [],
    [shadow],
  );

  // Search is client-side over the stable top-level shadow key; shadow keys
  // form a small structural set, so no pagination is needed.
  const filteredRows = useMemo(() => {
    const term = keyword.trim().toLowerCase();
    return term ? rows.filter((row) => row.key.toLowerCase().includes(term)) : rows;
  }, [rows, keyword]);

  const columns = [
    { title: t("common.key"), dataIndex: "key" },
    {
      title: t("pages.deviceDetail.shadow.desired"),
      dataIndex: "desired",
      render: (value: unknown) =>
        value === undefined ? "-" : <code>{JSON.stringify(value)}</code>,
    },
    {
      title: t("pages.deviceDetail.shadow.reported"),
      dataIndex: "reported",
      render: (value: unknown) =>
        value === undefined ? "-" : <code>{JSON.stringify(value)}</code>,
    },
    {
      title: t("common.status"),
      dataIndex: "inSync",
      render: (inSync: boolean) => (
        <Tag color={inSync ? "green" : "orange"}>
          {inSync
            ? t("pages.deviceDetail.shadow.synced")
            : t("pages.deviceDetail.shadow.pending")}
        </Tag>
      ),
    },
  ];

  return (
    <Card
      extra={
        <Space>
          {shadow && (
            <Tag color={shadow.in_sync ? "green" : "orange"}>
              {shadow.in_sync
                ? t("pages.deviceDetail.shadow.inSync")
                : t("pages.deviceDetail.shadow.outOfSync")}
            </Tag>
          )}
          <Typography.Text type="secondary">
            {t("pages.deviceDetail.shadow.versions", {
              desired: shadow?.desired_version ?? 0,
              reported: shadow?.reported_version ?? 0,
            })}
          </Typography.Text>
          <Button
            type="primary"
            icon={<EditOutlined />}
            onClick={openEditor}
          >
            {t("pages.deviceDetail.shadow.updateDesired")}
          </Button>
        </Space>
      }
    >
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t("pages.deviceDetail.shadow.searchPlaceholder")}
      />
      <Table
        rowKey="key"
        loading={query.isFetching}
        columns={columns}
        dataSource={filteredRows}
        pagination={false}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      <Drawer
        open={open}
        title={t("pages.deviceDetail.shadow.updateDesired")}
        width={760}
        onClose={() => setOpen(false)}
        destroyOnHidden
        footer={
          <Space style={{ justifyContent: "flex-end", width: "100%" }}>
            <Button onClick={() => setOpen(false)}>{t("common.cancel")}</Button>
            <Button
              type="primary"
              loading={mutation.isPending}
              onClick={() => mutation.mutate()}
            >
              {t("common.save")}
            </Button>
          </Space>
        }
      >
        <Typography.Text strong>
          {t("pages.deviceDetail.shadow.commonFields")}
        </Typography.Text>
        {catalogFields.length > 0 && (
          <Select
            showSearch
            allowClear
            style={{ width: "100%", marginTop: 8 }}
            placeholder={t("pages.deviceDetail.shadow.addCatalogKey")}
            value={pickKey}
            onChange={(key?: string) => {
              setPickKey(undefined);
              if (key) addCatalogField(key);
            }}
            options={catalogFields
              .filter((f) => !fields.some((row) => row.key === f.key))
              .map((f) => ({
                value: f.key,
                label: f.unit ? `${f.key} (${f.unit})` : f.key,
              }))}
            optionFilterProp="label"
          />
        )}
        <div style={{ marginTop: 8 }}>
          {fields.length === 0 && (
            <Empty
              image={Empty.PRESENTED_IMAGE_SIMPLE}
              description={
                catalogFields.length > 0
                  ? t("pages.deviceDetail.shadow.pickOrAddKey")
                  : t("pages.deviceDetail.shadow.noCatalogKeys")
              }
            />
          )}
          {fields.map((field) => (
            <Space
              key={field.id}
              align="start"
              style={{ display: "flex", marginBottom: 8 }}
            >
              <Input
                style={{ width: 180 }}
                placeholder={t("pages.deviceDetail.shadow.keyPlaceholder")}
                value={field.key}
                disabled={field.catalog}
                onChange={(e) => updateField(field.id, { key: e.target.value })}
              />
              {!field.catalog && (
                <Select<FieldValueKind>
                  style={{ width: 110 }}
                  value={field.kind}
                  onChange={(kind) => updateField(field.id, { kind })}
                  options={SCALAR_KINDS.map((kind) => ({
                    value: kind,
                    label: t(`pages.deviceDetail.shadow.type${kind.charAt(0).toUpperCase()}${kind.slice(1)}`),
                  }))}
                />
              )}
              {field.catalog && (
                <Tag style={{ width: 110, textAlign: "center", marginInlineEnd: 0 }}>
                  {t(`pages.deviceDetail.shadow.type${field.kind.charAt(0).toUpperCase()}${field.kind.slice(1)}`)}
                </Tag>
              )}
              {field.kind === "boolean" && (
                <Switch
                  checked={field.value as boolean}
                  checkedChildren={t("pages.deviceDetail.shadow.valueTrue")}
                  unCheckedChildren={t("pages.deviceDetail.shadow.valueFalse")}
                  onChange={(value) => updateField(field.id, { value })}
                />
              )}
              {field.kind === "number" && (
                <InputNumber
                  style={{ width: 240 }}
                  value={field.value as number}
                  min={field.min ?? undefined}
                  max={field.max ?? undefined}
                  addonAfter={field.unit || undefined}
                  onChange={(value) =>
                    updateField(field.id, { value: value ?? 0 })
                  }
                />
              )}
              {field.kind === "string" && (
                <Input
                  style={{ width: 240 }}
                  value={field.value as string}
                  onChange={(e) => updateField(field.id, { value: e.target.value })}
                />
              )}
              {(field.kind === "object" || field.kind === "array") && (
                <Input.TextArea
                  rows={3}
                  style={{ width: 320, fontFamily: "monospace" }}
                  value={field.value as string}
                  onChange={(e) => updateField(field.id, { value: e.target.value })}
                />
              )}
              <Button
                danger
                type="text"
                icon={<DeleteOutlined />}
                onClick={() => removeField(field.id)}
                aria-label={t("pages.deviceDetail.shadow.deleteKey")}
              />
            </Space>
          ))}
          <Button
            type="dashed"
            block
            icon={<PlusOutlined />}
            onClick={addField}
            style={{ marginTop: 4 }}
          >
            {t("pages.deviceDetail.shadow.addCustomKey")}
          </Button>
        </div>

        <Divider style={{ margin: "16px 0" }} />

        <Typography.Text strong>
          {t("pages.deviceDetail.shadow.extraJson")}
        </Typography.Text>
        <Input.TextArea
          rows={6}
          style={{ marginTop: 8, fontFamily: "monospace" }}
          value={extra}
          onChange={(e) => setExtra(e.target.value)}
        />
      </Drawer>
    </Card>
  );
}
