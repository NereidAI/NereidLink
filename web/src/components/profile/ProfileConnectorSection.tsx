// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ProfileConnectorSection edits the optional ADR-0026 connector template of
// one profile directly on the detail page (no nested modal). The connector
// type is a free-form AutoComplete preloaded with the built-in type and every
// type the connected gateways have registered; it is never forced to a
// specific implementation, so a third-party edge type is edited exactly like
// the built-in one. Selecting an EdgeX-family type enables the structured
// protocols/auto-events form through JsonEditor; any other type renders its
// registered JSON Schema or free JSON. Saving PUTs the whole profile via
// profileToInput, overriding only the connector fields.

import { App as AntApp, AutoComplete, Descriptions, Form, Space, Tag, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import type { Profile, ProfileInput } from "../../api/profiles";
import { updateProfile } from "../../api/profiles";
import { connectorTypeOptions, listConnectorSchemas } from "../../api/connectors";
import JsonEditor from "../connector/JsonConfigEditor";
import { sanitizeEdgeName } from "../connector/protocolDict";
import { useCatalogDictionary } from "../connector/useProtocolCatalog";
import {
  isEdgeConnectorType,
  parseConfigObject,
  parseMappings,
} from "../connector/jsonEditor";
import { profileToInput } from "../profilePayload";
import { extractError } from "../../api/client";
import { EditableSection } from "./EditableSection";

interface ConnectorFormValues {
  connector_type?: string;
  connector_type_version?: string;
  connector_config_template?: string;
  connector_mappings?: string;
}

interface ProfileConnectorSectionProps {
  // profile is the stored profile being edited.
  profile: Profile;
  // editing/onEdit/onCancel are the shared read/edit flag of this section.
  editing: boolean;
  onEdit: () => void;
  onCancel: () => void;
}

// ProfileConnectorSection renders the connector-template card.
export function ProfileConnectorSection({
  profile,
  editing,
  onEdit,
  onCancel,
}: ProfileConnectorSectionProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<ConnectorFormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const schemasQuery = useQuery({
    queryKey: ["connector-schemas"],
    queryFn: listConnectorSchemas,
  });

  const connectorType = Form.useWatch("connector_type", form);
  const connectorVersion = Form.useWatch("connector_type_version", form);
  const protocolFamily = profile.protocol;

  // Point-name suggestions mirror the full editor: descriptor measurement
  // keys drive auto-event sourceName and mapping key suggestions.
  const pointNameOptions = useMemo(
    () =>
      Array.from(
        new Set(
          (profile.descriptors ?? [])
            .map((d) => d.measurement_key?.trim())
            .filter((k): k is string => !!k),
        ),
      ),
    [profile.descriptors],
  );

  const connectorTypeOptionsList = useMemo(
    () => connectorTypeOptions(schemasQuery.data ?? []),
    [schemasQuery.data],
  );

  const connectorVersionOptions = useMemo(
    () =>
      (schemasQuery.data ?? [])
        .filter(
          (schema) =>
            schema.connector_type === connectorType && schema.connector_type_version,
        )
        .map((schema) => ({ value: schema.connector_type_version })),
    [schemasQuery.data, connectorType],
  );

  // Resolve the registered schema of the selected type/version, falling back
  // to the highest registered version when the version is blank (the backend
  // resolves it the same way).
  const configSchema = useMemo(() => {
    if (!connectorType) return null;
    const all = schemasQuery.data ?? [];
    const exact = all.find(
      (s) =>
        s.connector_type === connectorType &&
        s.connector_type_version === connectorVersion,
    );
    if (exact) return exact.config_schema;
    return (
      all
        .filter((s) => s.connector_type === connectorType)
        .sort((a, b) =>
          b.connector_type_version.localeCompare(a.connector_type_version),
        )[0]?.config_schema ?? null
    );
  }, [schemasQuery.data, connectorType, connectorVersion]);

  const catalogDict = useCatalogDictionary(protocolFamily);

  // One-time seed of a blank EdgeX template: ds_name from the family's
  // official device-service name and profile_name from the profile name.
  // device_name is deliberately not seeded (per-device, injected by the
  // connector-instance editor). The key identifies the seeding context.
  const edgeSeed = useMemo(() => {
    if (!isEdgeConnectorType(connectorType)) return { seedKey: "", seedDoc: null };
    const seed: Record<string, unknown> = {};
    const serviceName = catalogDict?.service_name;
    if (serviceName) seed.ds_name = serviceName;
    if (profile.name) seed.profile_name = sanitizeEdgeName(profile.name);
    return { seedKey: `${profile.id}::${connectorType}`, seedDoc: seed };
  }, [connectorType, catalogDict, profile.name, profile.id]);

  const mutation = useMutation({
    mutationFn: async (values: ConnectorFormValues) => {
      const input: ProfileInput = profileToInput(profile);
      const typeValue = values.connector_type?.trim();
      if (!typeValue) {
        // Clearing the type removes the whole connector template.
        input.connector_type = undefined;
        input.connector_type_version = undefined;
        input.connector_config_template = {};
        input.connector_mappings = [];
      } else {
        input.connector_type = typeValue;
        input.connector_type_version = values.connector_type_version?.trim() || "";
        const configResult = parseConfigObject(values.connector_config_template ?? "");
        if (!configResult.ok || !configResult.value) {
          throw new Error(
            `${t("pages.connectors.errors.configJson")}: ${configResult.error ?? ""}`,
          );
        }
        input.connector_config_template = configResult.value;
        const mappingsResult = parseMappings(values.connector_mappings ?? "");
        if (!mappingsResult.ok || !mappingsResult.value) {
          throw new Error(
            `${t("pages.connectors.errors.mappingsJson")}: ${mappingsResult.error ?? ""}`,
          );
        }
        input.connector_mappings = mappingsResult.value;
      }
      await updateProfile(profile.id, input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["profiles"] });
      message.success(t("pages.profiles.toasts.updated"));
      onCancel();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const hasTemplate = !!profile.connector_type;
  const configKeys = Object.keys(profile.connector_config_template ?? {});

  const readView = hasTemplate ? (
    <Descriptions column={1} size="small">
      <Descriptions.Item label={t("pages.connectors.form.type")}>
        <Tag color="geekblue">
          {profile.connector_type}
          {profile.connector_type_version ? `@${profile.connector_type_version}` : ""}
        </Tag>
      </Descriptions.Item>
      <Descriptions.Item label={t("pages.connectors.form.config")}>
        <Typography.Text code style={{ whiteSpace: "pre-wrap" }}>
          {configKeys.length ? JSON.stringify(profile.connector_config_template, null, 2) : "{}"}
        </Typography.Text>
      </Descriptions.Item>
      <Descriptions.Item label={t("pages.connectors.form.mappings")}>
        <Typography.Text code style={{ whiteSpace: "pre-wrap" }}>
          {(profile.connector_mappings ?? []).length
            ? JSON.stringify(profile.connector_mappings, null, 2)
            : "[]"}
        </Typography.Text>
      </Descriptions.Item>
    </Descriptions>
  ) : (
    <Typography.Text type="secondary">
      {t("pages.profiles.noConnectorTemplate")}
    </Typography.Text>
  );

  return (
    <EditableSection
      title={t("pages.profiles.detail.sections.connector")}
      editing={editing}
      onEdit={onEdit}
      onCancel={onCancel}
      readView={readView}
      form={form}
      saving={mutation.isPending}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        initialValues={{
          connector_type: profile.connector_type ?? "",
          connector_type_version: profile.connector_type_version ?? "",
          connector_config_template:
            profile.connector_config_template &&
            Object.keys(profile.connector_config_template).length > 0
              ? JSON.stringify(profile.connector_config_template, null, 2)
              : "",
          connector_mappings:
            profile.connector_mappings && profile.connector_mappings.length
              ? JSON.stringify(profile.connector_mappings, null, 2)
              : "",
        }}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Space wrap align="start" style={{ display: "flex" }}>
          <Form.Item
            label={t("pages.connectors.form.type")}
            name="connector_type"
            style={{ width: 280 }}
            extra={t("pages.profiles.form.connectorTypeHint")}
          >
            <AutoComplete
              options={connectorTypeOptionsList}
              placeholder="nereid-edge"
              allowClear
              filterOption
            />
          </Form.Item>
          <Form.Item
            label={t("pages.connectors.form.version")}
            name="connector_type_version"
            style={{ width: 220 }}
          >
            <AutoComplete
              options={connectorVersionOptions}
              placeholder={t("pages.connectors.form.versionHint")}
              allowClear
              filterOption
            />
          </Form.Item>
        </Space>
        <Form.Item
          label={t("pages.connectors.form.config")}
          name="connector_config_template"
        >
          <JsonEditor
            kind="config"
            connectorType={connectorType}
            schema={configSchema}
            partial
            protocolFamily={protocolFamily}
            seedDoc={edgeSeed.seedDoc}
            seedKey={edgeSeed.seedKey}
            deviceNameAutoHint={t(
              "pages.connectors.jsonEditor.deviceNameAuto",
            )}
            pointNameOptions={pointNameOptions}
          />
        </Form.Item>
        <Form.Item label={t("pages.connectors.form.mappings")} name="connector_mappings">
          <JsonEditor kind="mappings" pointNameOptions={pointNameOptions} />
        </Form.Item>
      </Form>
    </EditableSection>
  );
}
