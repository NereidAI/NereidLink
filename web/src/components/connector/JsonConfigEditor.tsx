// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// JsonConfigEditor provides bidirectional structured-form ⇄ raw-JSON editing
// for connector config objects and point-mapping arrays. The authoritative
// value passed through the antd Form stays the JSON TEXT, so existing form
// plumbing and validation keep working; the structured pane parses and
// re-serializes that text.
//
// The default layout is a split view (ADR-0032 console redesign): structured
// fields on the left and a read-only live JSON document on the right. For a
// connector instance bound to a profile template, the right pane previews the
// EFFECTIVE document (profile template deep-merged with the instance
// overrides, mirroring internal/connector merge semantics). A raw-JSON mode
// remains the escape hatch for keys no structured field covers; switching
// back is blocked until the text parses and satisfies the entry contract.

import { useEffect, useMemo, useRef, useState } from "react";
import {
  Alert,
  AutoComplete,
  Button,
  Card,
  Col,
  Divider,
  Input,
  InputNumber,
  Row,
  Segmented,
  Select,
  Space,
  Switch,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import { useQuery } from "@tanstack/react-query";

import { getEdgeProfile, listEdgeProfiles } from "../../api/edgeProfiles";
import {
  isEdgeConnectorType,
  parseConfigObject,
  parseMappings,
  validateAgainstSchema,
  mergeConfigObject,
  effectiveMappings,
} from "./jsonEditor";
import {
  connectionFieldsFor,
  useCatalogDictionary,
  useCatalogDictionaryForKey,
  type CatalogDictionary,
} from "./useProtocolCatalog";
import type { FieldSpec } from "../../api/protocolCatalog";
import { DictFieldInput } from "./DictFields";

// JsonEditorProps configures one editor field.
interface JsonEditorProps {
  // value/onChange are the antd Form-controlled JSON text.
  value?: string;
  onChange?: (value: string) => void;

  // kind selects the entry contract: config object or mappings array.
  kind: "config" | "mappings";

  // connectorType enables the EdgeX-specific config form.
  connectorType?: string;

  // schema is the edge-registered draft 2020-12 JSON Schema for config.
  schema?: Record<string, unknown> | null;

  // partial relaxes required-key validation for instance overrides.
  partial?: boolean;

  // deviceNameDefault is the canonical name used to fill a blank EdgeX
  // device_name.
  deviceNameDefault?: string;

  // deviceNameFallbackList, for a bulk create over many sub-devices, lists the
  // per-device EdgeX names that will be injected when device_name is blank;
  // the editor only shows this as guidance (injection happens on save).
  deviceNameFallbackList?: string[];

  // deviceNameAutoHint, in a reusable profile template, explains that a
  // blank device_name is assigned automatically per sub-device at instance
  // creation. Shown only when no concrete default/fallback is available.
  deviceNameAutoHint?: string;

  // gatewayId, when present, enables EdgeX-protocol-aware suggestions: the
  // profile name is picked from the gateway's cached EdgeX profiles and an
  // auto event's sourceName from that profile's device resources.
  gatewayId?: string;

  // protocolFamily is the profile's ADR-0032 protocol family; it turns the
  // generic protocol-property editor into dictionary-driven connection
  // fields (e.g. Modbus Address/Port/UnitID).
  protocolFamily?: string;

  // templateDoc is the bound profile template config used by an instance to
  // preview the effective merged document.
  templateDoc?: Record<string, unknown> | null;

  // templateMappings is the bound profile template mappings list.
  templateMappings?: unknown[] | null;

  // seedDoc, when present together with seedKey, provides one-time default
  // values for a blank profile template (ds_name/profile_name/device_name).
  // It is shallow-merged into an empty/missing field only; it never overwrites
  // an existing value and is not used by connector-instance editors. seedKey
  // identifies the seeding context so it re-applies when the modal target or
  // connector type changes.
  seedDoc?: Record<string, unknown> | null;
  seedKey?: string;

  // pointNameOptions are the profile descriptor measurement keys (ADR-0032:
  // EdgeX deviceResource name = mappings key = auto-event sourceName). They
  // drive a searchable suggestion list for the auto-event sourceName and the
  // mapping key, while both fields stay free-text. They are unioned with the
  // gateway-reported EdgeX resources when a gateway context is present.
  pointNameOptions?: string[];
}

// editMode is split (structured + live preview) or raw (free JSON text).
type EditMode = "split" | "raw";

// JsonEditor is the Form-controlled bidirectional editor.
export default function JsonEditor({
  value,
  onChange,
  kind,
  connectorType,
  schema,
  partial,
  deviceNameDefault,
  deviceNameFallbackList,
  deviceNameAutoHint,
  gatewayId,
  protocolFamily,
  templateDoc,
  templateMappings,
  seedDoc,
  seedKey,
  pointNameOptions,
}: JsonEditorProps) {
  const { t } = useTranslation();
  const [mode, setMode] = useState<EditMode>("split");
  const text = value ?? "";

  // Rendering parse is lenient for mappings: a freshly added row carries an
  // empty key and must still render so it can be filled in. The strict save
  // error is surfaced separately in the raw JSON pane and re-checked on
  // submit.
  const parseResult = useMemo(
    () =>
      kind === "config"
        ? parseConfigObject(text)
        : parseMappings(text, { lenient: true }),
    [kind, text],
  );

  const mappingsSaveResult = useMemo(
    () => (kind === "mappings" ? parseMappings(text) : null),
    [kind, text],
  );
  const mappingsSaveError =
    mappingsSaveResult && !mappingsSaveResult.ok ? mappingsSaveResult.error : null;

  const schemaError = useMemo(() => {
    if (kind !== "config" || !parseResult.ok || !parseResult.value) return null;
    return validateAgainstSchema(parseResult.value, schema ?? null, !!partial);
  }, [kind, parseResult, schema, partial]);

  const blocked = !parseResult.ok || (kind === "config" && !!schemaError);

  // While the JSON text cannot be parsed/validated the structured pane has
  // nothing to render, so force the raw mode (the split option is disabled
  // in the segmented control); fixing the text restores split automatically.
  const effectiveMode: EditMode = blocked ? "raw" : mode;

  const emit = (doc: unknown) => {
    onChange?.(
      kind === "config"
        ? JSON.stringify(doc ?? {}, null, 2)
        : JSON.stringify(doc ?? [], null, 2),
    );
  };

  // One-time seeding of a blank profile template: each seed field is applied
  // at most once per seeding context (modal target + connector type), and
  // only when that field is currently absent or empty. It therefore never
  // overwrites an existing value, and clearing a seeded field afterwards stays
  // cleared. Fields whose seed value is not yet known (e.g. derived device
  // name before the profile has a name) are absent from seedDoc and get
  // seeded once the parent supplies them. Connector-instance editors pass no
  // seedDoc, so they are never affected.
  const seededRef = useRef<{ key: string; fields: Set<string> }>({ key: "", fields: new Set() });
  useEffect(() => {
    if (kind !== "config" || !seedDoc || !seedKey) return;
    if (seededRef.current.key !== seedKey) {
      seededRef.current = { key: seedKey, fields: new Set() };
    }
    const parsed = parseConfigObject(text);
    if (!parsed.ok || !parsed.value) return;
    const current = parsed.value;
    const patch: Record<string, unknown> = {};
    let changed = false;
    for (const [k, v] of Object.entries(seedDoc)) {
      if (v === undefined || v === "") continue;
      if (seededRef.current.fields.has(k)) continue;
      seededRef.current.fields.add(k);
      const present = current[k] !== undefined && current[k] !== "";
      if (!present) {
        patch[k] = v;
        changed = true;
      }
    }
    if (changed) emit({ ...current, ...patch });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seedKey, seedDoc, text]);

  const configDoc =
    kind === "config" && parseResult.ok && parseResult.value
      ? (parseResult.value as Record<string, unknown>)
      : null;
  const mappingsDoc =
    kind === "mappings" && parseResult.ok && parseResult.value
      ? (parseResult.value as Record<string, unknown>[])
      : null;

  const edge = kind === "config" && isEdgeConnectorType(connectorType);

  // The right-hand preview: the edited document itself, or the effective
  // result of merging it over the profile template for an instance.
  const previewDoc = useMemo(() => {
    if (kind === "config") {
      if (templateDoc) {
        return mergeConfigObject(templateDoc, configDoc ?? {});
      }
      return configDoc ?? {};
    }
    return effectiveMappings(templateMappings ?? null, mappingsDoc ?? []);
  }, [kind, templateDoc, templateMappings, configDoc, mappingsDoc]);
  const isEffective = !!templateDoc || (kind === "mappings" && !!templateMappings);

  // EdgeX-aware suggestions, only meaningful for an edge config rendered in
  // the gateway detail (which owns the reported EdgeX profiles). A profile
  // template has no gateway context, so these stay undefined there and the
  // fields remain free-text AutoCompletes.
  const edgeProfileName =
    edge && configDoc && typeof configDoc.profile_name === "string"
      ? (configDoc.profile_name as string)
      : "";
  const edgeProfilesQuery = useQuery({
    queryKey: ["gateway-edge-profiles-names", gatewayId],
    queryFn: () => listEdgeProfiles(gatewayId as string),
    enabled: edge && !!gatewayId,
  });
  const edgeProfileDetailQuery = useQuery({
    queryKey: ["gateway-edge-profile", gatewayId, edgeProfileName],
    queryFn: () => getEdgeProfile(gatewayId as string, edgeProfileName),
    enabled: edge && !!gatewayId && !!edgeProfileName,
  });
  const edgeProfileNames =
    edgeProfilesQuery.data?.map((profile) => profile.name) ?? [];
  // EdgeX autoEvents address either a deviceResource name or a deviceCommand
  // name; the reported subset only carries resources, which is the normal
  // case for periodic reads. These are unioned with the bound profile's
  // descriptor measurement keys so suggestions are available even when the
  // gateway has not reported an EdgeX profile (e.g. a template editor).
  const edgeSourceNames = useMemo(
    () =>
      edgeProfileDetailQuery.data?.resources
        .filter((resource) => resource.translatable)
        .map((resource) => resource.name) ?? [],
    [edgeProfileDetailQuery.data],
  );
  const pointNames = useMemo(
    () => Array.from(new Set([...(pointNameOptions ?? []), ...edgeSourceNames])),
    [pointNameOptions, edgeSourceNames],
  );

  const structuredPane =
    !parseResult.ok || (kind === "config" && schemaError) ? (
      <Alert
        type="error"
        showIcon
        message={
          !parseResult.ok
            ? parseResult.error
            : (schemaError ?? t("pages.connectors.jsonEditor.invalid"))
        }
        action={
          <Button size="small" onClick={() => setMode("raw")}>
            {t("pages.connectors.jsonEditor.fixInJson")}
          </Button>
        }
      />
    ) : kind === "config" ? (
      edge ? (
        <EdgeConfigForm
          doc={configDoc ?? {}}
          emit={emit}
          deviceNameDefault={deviceNameDefault}
          deviceNameFallbackList={deviceNameFallbackList}
          deviceNameAutoHint={deviceNameAutoHint}
          required={!partial}
          profileNameOptions={edgeProfileNames}
          sourceNameOptions={pointNames}
          protocolFamily={protocolFamily}
        />
      ) : (
        <SchemaConfigForm doc={configDoc ?? {}} emit={emit} schema={schema} />
      )
    ) : (
      <MappingsForm entries={mappingsDoc ?? []} emit={emit} keyOptions={pointNames} />
    );

  return (
    <div>
      <Space style={{ marginBottom: 8 }}>
        <Segmented
          size="small"
          value={effectiveMode}
          onChange={(next) => setMode(next as EditMode)}
          options={[
            {
              value: "split",
              label: t("pages.connectors.jsonEditor.formTab"),
              disabled: blocked,
            },
            { value: "raw", label: t("pages.connectors.jsonEditor.jsonTab") },
          ]}
        />
        {effectiveMode === "split" && isEffective && (
          <Tag color="blue">{t("pages.connectors.jsonEditor.effectiveTag")}</Tag>
        )}
      </Space>
      {effectiveMode === "raw" ? (
        <>
          <Input.TextArea
            rows={kind === "config" ? 10 : 8}
            spellCheck={false}
            value={text}
            onChange={(event) => onChange?.(event.target.value)}
            style={{ fontFamily: "monospace", fontSize: 12 }}
          />
          {!parseResult.ok && (
            <Alert
              style={{ marginTop: 8 }}
              type="error"
              showIcon
              message={parseResult.error}
            />
          )}
          {kind === "mappings" && mappingsSaveError && (
            <Alert
              style={{ marginTop: 8 }}
              type="warning"
              showIcon
              message={mappingsSaveError}
            />
          )}
          {kind === "config" && schemaError && (
            <Alert
              style={{ marginTop: 8 }}
              type="warning"
              showIcon
              message={schemaError}
            />
          )}
        </>
      ) : (
        <Row gutter={12} align="stretch">
          <Col span={13}>{structuredPane}</Col>
          <Col span={11}>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {isEffective
                ? t("pages.connectors.jsonEditor.effectivePreview")
                : t("pages.connectors.jsonEditor.liveJson")}
            </Typography.Text>
            <pre
              style={{
                margin: "4px 0 0",
                padding: 8,
                background: "#fafafa",
                border: "1px solid #f0f0f0",
                borderRadius: 6,
                fontSize: 12,
                fontFamily: "monospace",
                maxHeight: 460,
                overflow: "auto",
                whiteSpace: "pre-wrap",
              }}
            >
              {JSON.stringify(previewDoc, null, 2)}
            </pre>
          </Col>
        </Row>
      )}
    </div>
  );
}

// updateDoc returns a shallow clone of doc with key set to next.
function updateDoc(doc: Record<string, unknown>, key: string, next: unknown): Record<string, unknown> {
  return { ...doc, [key]: next };
}

// EdgeConfigForm renders the structured EdgeX device connector form. Every
// field is controlled and serializes back into the same opaque config
// object NereidEdge turns into an EdgeX dtos.Device (ds_name=serviceName,
// profile_name, device_name, description, labels, protocols, auto_events).
function EdgeConfigForm({
  doc,
  emit,
  deviceNameDefault,
  deviceNameFallbackList,
  deviceNameAutoHint,
  required,
  profileNameOptions,
  sourceNameOptions,
  protocolFamily,
}: {
  doc: Record<string, unknown>;
  emit: (doc: unknown) => void;
  deviceNameDefault?: string;
  deviceNameFallbackList?: string[];
  deviceNameAutoHint?: string;
  required: boolean;
  profileNameOptions: string[];
  sourceNameOptions: string[];
  protocolFamily?: string;
}) {
  const { t } = useTranslation();
  const set = (key: string, next: unknown) => emit(updateDoc(doc, key, next));

  // status warns on a missing field only for a full config document
  // (template or template-less instance); a partial override may omit keys.
  const warn = (present: unknown) =>
    required && (present === undefined || present === "") ? "warning" : undefined;

  const labels = Array.isArray(doc.labels) ? (doc.labels as unknown[]) : [];
  const protocols = isObject(doc.protocols) ? (doc.protocols as Record<string, unknown>) : {};
  const autoEvents = Array.isArray(doc.auto_events)
    ? (doc.auto_events as Record<string, unknown>[])
    : [];

  return (
    <Space direction="vertical" size={8} style={{ display: "flex" }}>
      <FieldRow label={t("pages.connectors.jsonEditor.dsName")} required={required}>
        <Input
          value={(doc.ds_name as string) ?? ""}
          status={warn(doc.ds_name)}
          onChange={(event) => set("ds_name", event.target.value)}
        />
      </FieldRow>
      <FieldRow label={t("pages.connectors.jsonEditor.profileName")} required={required}>
        <SuggestInput
          value={(doc.profile_name as string) ?? ""}
          options={profileNameOptions}
          status={warn(doc.profile_name)}
          onChange={(value) => set("profile_name", value)}
        />
      </FieldRow>
      <FieldRow
        label={t("pages.connectors.jsonEditor.deviceName")}
        required={required}
        hint={
          deviceNameFallbackList && deviceNameFallbackList.length > 1
            ? t("pages.connectors.jsonEditor.deviceNameMulti", {
                count: deviceNameFallbackList.length,
                names: deviceNameFallbackList.slice(0, 3).join(", "),
              })
            : deviceNameDefault
              ? t("pages.connectors.jsonEditor.deviceNameDefault", { name: deviceNameDefault })
              : deviceNameAutoHint
        }
      >
        <Input
          value={(doc.device_name as string) ?? ""}
          placeholder={
            deviceNameFallbackList && deviceNameFallbackList.length > 1
              ? t("pages.connectors.jsonEditor.deviceNameMultiPlaceholder")
              : deviceNameDefault
          }
          status={warn(doc.device_name)}
          onChange={(event) => set("device_name", event.target.value)}
        />
      </FieldRow>
      <FieldRow label={t("pages.connectors.jsonEditor.description")}>
        <Input.TextArea
          rows={2}
          value={(doc.description as string) ?? ""}
          onChange={(event) => set("description", event.target.value)}
        />
      </FieldRow>
      <FieldRow label={t("pages.connectors.jsonEditor.labels")}>
        <StringListEditor
          values={labels.map((v) => String(v))}
          onChange={(next) => set("labels", next)}
        />
      </FieldRow>
      <Divider style={{ margin: "4px 0" }} orientation="left" plain>
        {t("pages.connectors.jsonEditor.protocols")}
      </Divider>
      <ProtocolEditor
        protocols={protocols}
        onChange={(next) => set("protocols", next)}
        family={protocolFamily}
      />
      <Divider style={{ margin: "4px 0" }} orientation="left" plain>
        {t("pages.connectors.jsonEditor.autoEvents")}
      </Divider>
      <AutoEventsEditor
        events={autoEvents}
        onChange={(next) => set("auto_events", next)}
        sourceNameOptions={sourceNameOptions}
      />
      {(doc.tags !== undefined || doc.properties !== undefined) && (
        <Alert
          type="info"
          showIcon
          style={{ marginTop: 4 }}
          message={t("pages.connectors.jsonEditor.advancedInJson")}
        />
      )}
    </Space>
  );
}

// SuggestInput is a free-text AutoComplete: the operator can pick an offered
// option (EdgeX profile/resource names) or type a custom value.
function SuggestInput({
  value,
  options,
  status,
  placeholder,
  onChange,
}: {
  value: string;
  options: string[];
  status?: "" | "warning" | "error";
  placeholder?: string;
  onChange: (value: string) => void;
}) {
  return (
    <AutoComplete
      style={{ width: "100%" }}
      value={value}
      status={status}
      placeholder={placeholder}
      options={options.map((option) => ({ value: option }))}
      filterOption={(input, option) =>
        String(option?.value ?? "").toLowerCase().includes(input.toLowerCase())
      }
      onChange={onChange}
      allowClear
    />
  );
}

function parseLooseObject(text: string): Record<string, unknown> | null {
  if (!text.trim()) return {};
  try {
    const parsed = JSON.parse(text) as unknown;
    return isObject(parsed) ? (parsed as Record<string, unknown>) : null;
  } catch {
    return null;
  }
}

// SchemaConfigForm renders config from a registered JSON Schema; with no
// schema it falls back to a generic key/JSON-literal table.
function SchemaConfigForm({
  doc,
  emit,
  schema,
}: {
  doc: Record<string, unknown>;
  emit: (doc: unknown) => void;
  schema?: Record<string, unknown> | null;
}) {
  const { t } = useTranslation();
  const properties = isObject(schema?.properties)
    ? (schema!.properties as Record<string, Record<string, unknown>>)
    : null;
  const required = Array.isArray(schema?.required)
    ? ((schema!.required as unknown[]).map(String))
    : [];

  if (!properties) {
    return <GenericObjectEditor doc={doc} emit={emit} />;
  }
  return (
    <Space direction="vertical" size={8} style={{ display: "flex" }}>
      {Object.entries(properties).map(([name, sub]) => (
        <SchemaField
          key={name}
          name={name}
          schema={sub ?? {}}
          required={required.includes(name)}
          value={doc[name]}
          onChange={(next) => emit(updateDoc(doc, name, next))}
        />
      ))}
      {!schema?.additionalProperties && (
        <Typography.Text type="secondary" style={{ fontSize: 12 }}>
          {t("pages.connectors.jsonEditor.extraInJson")}
        </Typography.Text>
      )}
    </Space>
  );
}

// SchemaField renders one property from a JSON Schema.
function SchemaField({
  name,
  schema,
  required,
  value,
  onChange,
}: {
  name: string;
  schema: Record<string, unknown>;
  required: boolean;
  value: unknown;
  onChange: (value: unknown) => void;
}) {
  const type = String(schema.type ?? "");
  const label = String(schema.title ?? name);
  const description = schema.description ? String(schema.description) : undefined;
  const enumValues = Array.isArray(schema.enum) ? (schema.enum as unknown[]) : null;

  let control: JSX.Element;
  if (enumValues) {
    control = (
      <Select
        allowClear
        style={{ width: "100%" }}
        value={(value as string) ?? undefined}
        placeholder={label}
        options={enumValues.map((v) => ({ value: String(v), label: String(v) }))}
        onChange={(next) => onChange(next ?? undefined)}
      />
    );
  } else if (type === "integer" || type === "number") {
    control = (
      <InputNumber
        style={{ width: "100%" }}
        value={typeof value === "number" ? value : undefined}
        onChange={(next) => onChange(next ?? undefined)}
      />
    );
  } else if (type === "boolean") {
    control = <Switch checked={value === true} onChange={onChange} />;
  } else if (type === "object") {
    control = (
      <Input.TextArea
        rows={3}
        spellCheck={false}
        style={{ fontFamily: "monospace", fontSize: 12 }}
        value={value === undefined ? "" : JSON.stringify(value, null, 2)}
        onChange={(event) => {
          const parsed = parseLooseObject(event.target.value);
          if (parsed) onChange(parsed);
        }}
      />
    );
  } else if (type === "array") {
    control = (
      <Input.TextArea
        rows={3}
        spellCheck={false}
        style={{ fontFamily: "monospace", fontSize: 12 }}
        value={value === undefined ? "" : JSON.stringify(value, null, 2)}
        onChange={(event) => {
          if (!event.target.value.trim()) {
            onChange(undefined);
            return;
          }
          try {
            onChange(JSON.parse(event.target.value));
          } catch {
            // Ignore intermediate invalid states while typing.
          }
        }}
      />
    );
  } else {
    control = (
      <Input
        value={value === undefined || value === null ? "" : String(value)}
        onChange={(event) => onChange(event.target.value)}
      />
    );
  }
  return (
    <FieldRow label={label} required={required} hint={description}>
      {control}
    </FieldRow>
  );
}

// GenericObjectEditor is the fallback without an EdgeX form or a schema:
// key plus JSON-literal value rows.
function GenericObjectEditor({
  doc,
  emit,
}: {
  doc: Record<string, unknown>;
  emit: (doc: unknown) => void;
}) {
  const { t } = useTranslation();
  const entries = Object.entries(doc);
  const renameKey = (oldKey: string, newKey: string) => {
    if (!newKey || oldKey === newKey) return;
    const next: Record<string, unknown> = {};
    for (const [k, v] of entries) next[k === oldKey ? newKey : k] = v;
    emit(next);
  };
  const setValue = (key: string, raw: string) => {
    let parsed: unknown = raw;
    if (raw.trim()) {
      try {
        parsed = JSON.parse(raw);
      } catch {
        parsed = raw;
      }
    }
    emit(updateDoc(doc, key, parsed));
  };
  return (
    <Space direction="vertical" size={8} style={{ display: "flex" }}>
      {entries.map(([key, val]) => (
        <Space.Compact key={key} style={{ width: "100%" }}>
          <Input
            style={{ width: 180 }}
            defaultValue={key}
            onBlur={(event) => renameKey(key, event.target.value)}
          />
          <Input
            style={{ fontFamily: "monospace", fontSize: 12 }}
            defaultValue={JSON.stringify(val)}
            onBlur={(event) => setValue(key, event.target.value)}
          />
          <Button
            danger
            icon={<DeleteOutlined />}
            onClick={() => {
              const next = { ...doc };
              delete next[key];
              emit(next);
            }}
          />
        </Space.Compact>
      ))}
      <Button
        type="dashed"
        block
        icon={<PlusOutlined />}
        onClick={() => {
          let candidate = "key";
          let n = 1;
          while (candidate in doc) {
            n += 1;
            candidate = `key_${n}`;
          }
          emit(updateDoc(doc, candidate, ""));
        }}
      >
        {t("pages.connectors.jsonEditor.addProperty")}
      </Button>
    </Space>
  );
}

// MappingsForm renders the point-mapping array: each row requires a string
// key (suggested from the profile descriptor keys, still free text) and
// exposes its remaining properties as name/value pairs.
function MappingsForm({
  entries,
  emit,
  keyOptions,
}: {
  entries: Record<string, unknown>[];
  emit: (entries: unknown) => void;
  keyOptions?: string[];
}) {
  const { t } = useTranslation();
  const updateEntry = (index: number, next: Record<string, unknown>) => {
    const copy = [...entries];
    copy[index] = next;
    emit(copy);
  };
  return (
    <Space direction="vertical" size={8} style={{ display: "flex" }}>
      {entries.map((entry, index) => (
        <Card
          key={index}
          size="small"
          title={`#${index + 1}`}
          extra={
            <Button
              type="text"
              danger
              icon={<DeleteOutlined />}
              onClick={() => emit(entries.filter((_, i) => i !== index))}
            />
          }
        >
          <FieldRow label="key" required>
            <SuggestInput
              value={String(entry.key ?? "")}
              options={keyOptions ?? []}
              status={!entry.key ? "warning" : undefined}
              onChange={(value) => updateEntry(index, { ...entry, key: value })}
            />
          </FieldRow>
          {Object.entries(entry)
            .filter(([k]) => k !== "key")
            .map(([propKey, propVal]) => (
              <Space.Compact key={propKey} style={{ width: "100%", marginTop: 8 }}>
                <Input
                  style={{ width: 160 }}
                  defaultValue={propKey}
                  onBlur={(event) => {
                    const next = renameProp(entry, propKey, event.target.value);
                    updateEntry(index, next);
                  }}
                />
                <Input
                  style={{ fontFamily: "monospace", fontSize: 12 }}
                  defaultValue={JSON.stringify(propVal)}
                  onBlur={(event) => {
                    let parsed: unknown = event.target.value;
                    try {
                      parsed = JSON.parse(event.target.value);
                    } catch {
                      parsed = event.target.value;
                    }
                    updateEntry(index, { ...entry, [propKey]: parsed });
                  }}
                />
                <Button
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => {
                    const next = { ...entry };
                    delete next[propKey];
                    updateEntry(index, next);
                  }}
                />
              </Space.Compact>
            ))}
          <Button
            type="dashed"
            size="small"
            icon={<PlusOutlined />}
            style={{ marginTop: 8 }}
            onClick={() =>
              updateEntry(index, {
                ...entry,
                [`prop_${Object.keys(entry).length}`]: "",
              })
            }
          >
            {t("pages.connectors.jsonEditor.addProperty")}
          </Button>
        </Card>
      ))}
      <Button
        type="dashed"
        block
        icon={<PlusOutlined />}
        onClick={() => emit([...entries, { key: "" }])}
      >
        {t("pages.connectors.jsonEditor.addMapping")}
      </Button>
    </Space>
  );
}

// renameProp renames an object property preserving order.
function renameProp(
  entry: Record<string, unknown>,
  oldKey: string,
  newKey: string,
): Record<string, unknown> {
  if (!newKey || oldKey === newKey) return entry;
  const out: Record<string, unknown> = {};
  for (const [k, v] of Object.entries(entry)) {
    out[k === oldKey ? newKey : k] = v;
  }
  return out;
}

// StringListEditor edits an array of plain strings.
function StringListEditor({
  values,
  onChange,
}: {
  values: string[];
  onChange: (values: string[]) => void;
}) {
  return (
    <Space direction="vertical" size={4} style={{ display: "flex" }}>
      {values.map((value, i) => (
        <Space.Compact key={i} style={{ width: "100%" }}>
          <Input
            defaultValue={value}
            onBlur={(event) => {
              const next = [...values];
              next[i] = event.target.value;
              onChange(next);
            }}
          />
          <Button
            danger
            icon={<DeleteOutlined />}
            onClick={() => onChange(values.filter((_, idx) => idx !== i))}
          />
        </Space.Compact>
      ))}
      <Button
        type="dashed"
        size="small"
        icon={<PlusOutlined />}
        onClick={() => onChange([...values, ""])}
      />
    </Space>
  );
}

// EDGE_PROTOCOL_NAMES are the protocol keys EdgeX device services use inside
// Device.Protocols (EdgeX docs/examples). A custom key is still allowed:
// EdgeX treats the map as protocol-name -> properties and does not constrain
// the key, and NereidLink stores config opaquely.
const EDGE_PROTOCOL_NAMES = [
  "modbus-tcp",
  "modbus-rtu",
  "modbus-ascii",
  "opcua",
  "mqtt",
  "virtual",
  "rest",
  "coap",
  "bacnet-ip",
  "bacnet-mstp",
  "s7",
  "ethernet-ip",
  "onvif",
  "snmp",
  "custom",
];

// ProtocolEditor edits protocols: EdgeX protocol name -> property map. When a
// protocol-family dictionary exists for the profile, it renders structured
// connection parameters per transport (e.g. Modbus Address/Port/UnitID plus
// serial fields on RTU/ASCII); unknown families keep the free-form typed
// property editor. Extra property keys a structured field does not cover stay
// editable as raw pairs so no data is ever stranded.
function ProtocolEditor({
  protocols,
  onChange,
  family,
}: {
  protocols: Record<string, unknown>;
  onChange: (protocols: Record<string, unknown>) => void;
  family?: string;
}) {
  const { t } = useTranslation();
  // Resolve the catalog family from the profile family when known, otherwise
  // from the first protocol-map key (e.g. an imported device with no family).
  const firstKey = Object.keys(protocols)[0];
  const dictFromFamily = useCatalogDictionary(family);
  const dictFromKey = useCatalogDictionaryForKey(firstKey);
  const dict = dictFromFamily ?? dictFromKey;

  const rename = (oldName: string, newName: string) => {
    const trimmed = newName.trim();
    if (!trimmed || trimmed === oldName || trimmed in protocols) return;
    const next: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(protocols)) {
      next[k === oldName ? trimmed : k] = v;
    }
    // Preserve insertion order with the renamed key where it was.
    onChange(next);
  };

  const nameOptions = (
    dict?.transports ?? EDGE_PROTOCOL_NAMES.map((value) => ({ value, label: value }))
  ).map((option) => ({ value: option.value }));

  const addKey = () =>
    dict && dict.transports.length > 0
      ? dict.transports[0].value
      : `protocol_${Object.keys(protocols).length + 1}`;

  return (
    <Space direction="vertical" size={8} style={{ display: "flex" }}>
      {Object.entries(protocols).map(([name, props]) => (
        <Card
          key={name}
          size="small"
          title={
            dict ? (
              <Select
                size="small"
                style={{ width: 220 }}
                value={name}
                options={dict.transports.map((transport) => ({
                  value: transport.value,
                  label: transport.label,
                }))}
                onChange={(value) => rename(name, value)}
              />
            ) : (
              <AutoComplete
                size="small"
                style={{ width: 220 }}
                defaultValue={name}
                options={nameOptions}
                filterOption={(input, option) =>
                  String(option?.value ?? "")
                    .toLowerCase()
                    .includes(input.toLowerCase())
                }
                onBlur={(event) =>
                  rename(name, (event.target as HTMLInputElement).value)
                }
                onSelect={(value) => rename(name, value)}
              />
            )
          }
          extra={
            <Button
              type="text"
              danger
              size="small"
              icon={<DeleteOutlined />}
              onClick={() => {
                const next = { ...protocols };
                delete next[name];
                onChange(next);
              }}
            />
          }
        >
          {dict ? (
            <DictConnectionFields
              dict={dict}
              transport={name}
              props={isObject(props) ? (props as Record<string, unknown>) : {}}
              onChange={(next) => onChange({ ...protocols, [name]: next })}
            />
          ) : (
            <TypedPropertyMap
              map={isObject(props) ? (props as Record<string, unknown>) : {}}
              onChange={(next) => onChange({ ...protocols, [name]: next })}
            />
          )}
        </Card>
      ))}
      <Button
        type="dashed"
        size="small"
        icon={<PlusOutlined />}
        onClick={() => {
          const key = addKey();
          if (!(key in protocols)) onChange({ ...protocols, [key]: {} });
        }}
      >
        {t("pages.connectors.jsonEditor.addProtocol")}
      </Button>
    </Space>
  );
}

// DictConnectionFields renders the catalog connection parameters for one
// transport, followed by any extra (non-catalog) properties as editable
// raw pairs.
function DictConnectionFields({
  dict,
  transport,
  props,
  onChange,
}: {
  dict: CatalogDictionary;
  transport: string;
  props: Record<string, unknown>;
  onChange: (props: Record<string, unknown>) => void;
}) {
  const fields = connectionFieldsFor(dict, transport);
  const known = new Set(fields.map((field) => field.key));
  const extra = Object.fromEntries(Object.entries(props).filter(([k]) => !known.has(k)));
  const setValue = (key: string, value: unknown) => {
    const next = { ...props };
    if (value === undefined) delete next[key];
    else next[key] = value;
    onChange(next);
  };

  return (
    <Space direction="vertical" size={6} style={{ display: "flex" }}>
      <Row gutter={[8, 6]}>
        {fields.map((field: FieldSpec) => (
          <Col key={field.key} span={field.key === "Address" ? 12 : 6}>
            <DictFieldInput
              spec={field}
              value={props[field.key]}
              onChange={(value) => setValue(field.key, value)}
            />
          </Col>
        ))}
      </Row>
      {Object.keys(extra).length > 0 && (
        <TypedPropertyMap map={extra} onChange={(nextExtra) => {
          const next: Record<string, unknown> = {};
          for (const [k, v] of Object.entries(props)) if (known.has(k)) next[k] = v;
          Object.assign(next, nextExtra);
          onChange(next);
        }} />
      )}
    </Space>
  );
}

// valueType tags how one protocol property is edited/serialized.
type ValueType = "string" | "number" | "boolean";

// typeOfValue infers the editor type from a stored JSON value.
function typeOfValue(value: unknown): ValueType {
  if (typeof value === "boolean") return "boolean";
  if (typeof value === "number") return "number";
  return "string";
}

// TypedPropertyMap edits a protocol property map whose values are typed
// (string/number/boolean), matching EdgeX ProtocolProperties values instead
// of forcing everything through strings.
function TypedPropertyMap({
  map,
  onChange,
}: {
  map: Record<string, unknown>;
  onChange: (map: Record<string, unknown>) => void;
}) {
  const pairs = Object.entries(map);
  const setValue = (key: string, value: unknown) => onChange({ ...map, [key]: value });
  const changeType = (key: string, nextType: ValueType) => {
    const current = map[key];
    let converted: unknown = "";
    if (nextType === "boolean") {
      converted = current === true || String(current).toLowerCase() === "true";
    } else if (nextType === "number") {
      const parsed = Number(current);
      converted = Number.isFinite(parsed) ? parsed : 0;
    } else {
      converted = typeof current === "object" ? JSON.stringify(current) : String(current ?? "");
    }
    setValue(key, converted);
  };

  return (
    <Space direction="vertical" size={4} style={{ display: "flex" }}>
      {pairs.map(([key, value]) => {
        const valueType = typeOfValue(value);
        return (
          <Space.Compact key={key} style={{ width: "100%" }}>
            <Input
              style={{ width: 150 }}
              defaultValue={key}
              onBlur={(event) => {
                const newKey = event.target.value;
                if (!newKey || newKey === key) return;
                const next: Record<string, unknown> = {};
                for (const [k, v] of pairs) next[k === key ? newKey : k] = v;
                onChange(next);
              }}
            />
            <Select
              style={{ width: 100 }}
              value={valueType}
              onChange={(nextType) => changeType(key, nextType)}
              options={[
                { value: "string", label: "string" },
                { value: "number", label: "number" },
                { value: "boolean", label: "boolean" },
              ]}
            />
            {valueType === "boolean" ? (
              <Switch
                style={{ width: 120, margin: "0 8px" }}
                checked={value === true}
                onChange={(checked) => setValue(key, checked)}
              />
            ) : valueType === "number" ? (
              <InputNumber
                style={{ flex: 1 }}
                value={typeof value === "number" ? value : Number(value) || 0}
                onChange={(num) => setValue(key, num ?? 0)}
              />
            ) : (
              <Input
                value={displayScalar(value)}
                onChange={(event) => setValue(key, event.target.value)}
              />
            )}
            <Button
              danger
              icon={<DeleteOutlined />}
              onClick={() => {
                const next = { ...map };
                delete next[key];
                onChange(next);
              }}
            />
          </Space.Compact>
        );
      })}
      <Button
        type="dashed"
        size="small"
        icon={<PlusOutlined />}
        onClick={() => onChange({ ...map, [`prop_${pairs.length + 1}`]: "" })}
      />
    </Space>
  );
}

// AutoEventsEditor edits EdgeX auto_events following the v4 AutoEvent DTO:
// interval (duration), sourceName (deviceResource/command), onChange,
// onChangeThreshold and retention {maxCap,minCap,duration}. sourceName is
// suggested from the selected EdgeX profile's resources but stays free text.
function AutoEventsEditor({
  events,
  onChange,
  sourceNameOptions,
}: {
  events: Record<string, unknown>[];
  onChange: (events: Record<string, unknown>[]) => void;
  sourceNameOptions: string[];
}) {
  const { t } = useTranslation();
  const [advanced, setAdvanced] = useState<Record<number, boolean>>({});

  // sanitize keeps the emitted payload close to EdgeX's own serialization:
  // omit onChange while it is the zero value and drop an all-zero/empty
  // retention object, so the edge never receives meaningless placeholders.
  const emitEvents = (next: Record<string, unknown>[]) => {
    onChange(
      next.map((event) => {
        const clean: Record<string, unknown> = {};
        for (const [k, v] of Object.entries(event)) {
          if (k === "onChange" && v !== true) continue;
          if (k === "onChangeThreshold" && event.onChange !== true) continue;
          if (k === "retention") {
            const r = isObject(v) ? v : {};
            const hasRetention =
              String(r.duration ?? "") !== "" ||
              Number(r.maxCap ?? 0) !== 0 ||
              Number(r.minCap ?? 0) !== 0;
            if (hasRetention) clean[k] = v;
            continue;
          }
          clean[k] = v;
        }
        return clean;
      }),
    );
  };

  const update = (i: number, patch: Record<string, unknown>) =>
    emitEvents(events.map((x, idx) => (idx === i ? { ...x, ...patch } : x)));

  return (
    <Space direction="vertical" size={8} style={{ display: "flex" }}>
      {events.map((event, i) => {
        const onChangeFlag = event.onChange === true;
        const retention = isObject(event.retention)
          ? (event.retention as Record<string, unknown>)
          : {};
        const setRetention = (patch: Record<string, unknown>) =>
          update(i, { retention: { ...retention, ...patch } });
        return (
          <Card
            key={i}
            size="small"
            title={`#${i + 1}`}
            extra={
              <Button
                type="text"
                danger
                size="small"
                icon={<DeleteOutlined />}
                onClick={() => onChange(events.filter((_, idx) => idx !== i))}
              />
            }
          >
            <Space direction="vertical" size={6} style={{ display: "flex" }}>
              <Space.Compact block>
                <Input
                  style={{ width: 100 }}
                  value={t("pages.connectors.jsonEditor.interval")}
                  disabled
                />
                <Input
                  style={{ width: 110 }}
                  placeholder="10s"
                  value={String(event.interval ?? "")}
                  onChange={(e) => update(i, { interval: e.target.value })}
                />
                <SuggestInput
                  value={String(event.sourceName ?? "")}
                  options={sourceNameOptions}
                  placeholder={t("pages.connectors.jsonEditor.sourceName")}
                  onChange={(value) => update(i, { sourceName: value })}
                />
              </Space.Compact>
              <Space>
                <Typography.Text style={{ fontSize: 12 }}>
                  {t("pages.connectors.jsonEditor.onChange")}
                </Typography.Text>
                <Switch
                  checked={onChangeFlag}
                  onChange={(checked) => update(i, { onChange: checked })}
                />
                {onChangeFlag && (
                  <>
                    <Typography.Text style={{ fontSize: 12 }}>
                      {t("pages.connectors.jsonEditor.onChangeThreshold")}
                    </Typography.Text>
                    <InputNumber
                      style={{ width: 120 }}
                      min={0}
                      value={
                        typeof event.onChangeThreshold === "number"
                          ? event.onChangeThreshold
                          : Number(event.onChangeThreshold) || 0
                      }
                      onChange={(num) => update(i, { onChangeThreshold: num ?? 0 })}
                    />
                  </>
                )}
                <Button
                  size="small"
                  type="link"
                  onClick={() => setAdvanced((s) => ({ ...s, [i]: !s[i] }))}
                >
                  {t("pages.connectors.jsonEditor.retention")}
                </Button>
              </Space>
              {advanced[i] && (
                <Space wrap>
                  <Space.Compact>
                    <Input
                      style={{ width: 92 }}
                      value={t("pages.connectors.jsonEditor.retentionMax")}
                      disabled
                    />
                    <InputNumber
                      style={{ width: 110 }}
                      min={0}
                      value={
                        typeof retention.maxCap === "number"
                          ? retention.maxCap
                          : Number(retention.maxCap) || 0
                      }
                      onChange={(num) => setRetention({ maxCap: num ?? 0 })}
                    />
                  </Space.Compact>
                  <Space.Compact>
                    <Input
                      style={{ width: 92 }}
                      value={t("pages.connectors.jsonEditor.retentionMin")}
                      disabled
                    />
                    <InputNumber
                      style={{ width: 110 }}
                      min={0}
                      value={
                        typeof retention.minCap === "number"
                          ? retention.minCap
                          : Number(retention.minCap) || 0
                      }
                      onChange={(num) => setRetention({ minCap: num ?? 0 })}
                    />
                  </Space.Compact>
                  <Space.Compact>
                    <Input
                      style={{ width: 92 }}
                      value={t("pages.connectors.jsonEditor.retentionDuration")}
                      disabled
                    />
                    <Input
                      style={{ width: 120 }}
                      placeholder="1h"
                      value={String(retention.duration ?? "")}
                      onChange={(e) => setRetention({ duration: e.target.value })}
                    />
                  </Space.Compact>
                </Space>
              )}
            </Space>
          </Card>
        );
      })}
      <Button
        type="dashed"
        size="small"
        icon={<PlusOutlined />}
        onClick={() =>
          emitEvents([
            ...events,
            { interval: "10s", sourceName: "" },
          ])
        }
      >
        {t("pages.connectors.jsonEditor.addAutoEvent")}
      </Button>
    </Space>
  );
}

// FieldRow renders a label above its control.
function FieldRow({
  label,
  required,
  hint,
  children,
}: {
  label: string;
  required?: boolean;
  hint?: string;
  children: React.ReactNode;
}) {
  return (
    <div>
      <Typography.Text style={{ fontSize: 12 }}>
        {required ? <span style={{ color: "#ff4d4f" }}>* </span> : null}
        {label}
      </Typography.Text>
      <div style={{ marginTop: 2 }}>{children}</div>
      {hint && (
        <Typography.Text type="secondary" style={{ fontSize: 11 }}>
          {hint}
        </Typography.Text>
      )}
    </div>
  );
}

// isObject reports whether value is a non-null, non-array object.
function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// displayScalar renders a stored property value in the string input; complex
// JSON values are shown verbatim so they are never flattened to
// "[object Object]".
function displayScalar(value: unknown): string {
  if (typeof value === "object" && value !== null) return JSON.stringify(value);
  return String(value ?? "");
}
