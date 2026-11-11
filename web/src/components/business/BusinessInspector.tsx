// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// BusinessInspector keeps flat template parameters and nested asset settings
// in separate forms. SchemaFields is used only for the template's flat global
// JSON Schema; camera, model, external-servable, and other nested assets use
// explicit controls so their deployment contracts are not silently flattened.

import { useEffect, useMemo } from "react";
import {
  Alert,
  AutoComplete,
  Divider,
  Form,
  Input,
  InputNumber,
  Select,
  Typography,
} from "antd";
import { useQuery } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import type { Assembly, BusinessNode } from "../../api/business";
import { extractError } from "../../api/client";
import { listCompanionEndpoints } from "../../api/companions";
import {
  listCommandCatalog,
  listDevices,
  listDevicesByGateway,
  listGatewayDevices,
  type Device,
} from "../../api/devices";
import { listForwarders } from "../../api/forwarders";
import { listGatewayModels } from "../../api/gatewayModels";
import { INFERENCE_TARGET_DEVICE_OPTIONS } from "../device/inferenceBinding";
import SchemaFields from "../schema/SchemaFields";
import { hasRenderableSchema } from "../schema/schemaFieldsUtils";
import {
  availableCompanionIds,
  businessTargetGatewayIds,
  intersectReadyGatewayModels,
} from "./businessModel";

interface BusinessInspectorProps {
  node?: BusinessNode;
  assembly: Assembly;
  parameterSchema: Record<string, unknown>;
  parameters: Record<string, unknown>;
  showParameters?: boolean;
  onNodeChange: (node: BusinessNode) => void;
  onParametersChange: (parameters: Record<string, unknown>) => void;
}

// flatParameterSchema removes object/array properties before SchemaFields sees
// the template. Nested assets keep their dedicated forms and can never degrade
// into string inputs merely because a template declares them under parameters.
function flatParameterSchema(
  schema: Record<string, unknown>,
): Record<string, unknown> {
  const rawProperties = schema.properties;
  if (!rawProperties || typeof rawProperties !== "object" || Array.isArray(rawProperties)) {
    return schema;
  }
  const properties: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(rawProperties)) {
    if (!value || typeof value !== "object" || Array.isArray(value)) continue;
    const property = value as Record<string, unknown>;
    const types = Array.isArray(property.type) ? property.type : [property.type];
    if (
      types.includes("object") ||
      types.includes("array") ||
      property.properties !== undefined ||
      property.items !== undefined
    ) {
      continue;
    }
    properties[key] = value;
  }
  const required = Array.isArray(schema.required)
    ? schema.required.filter(
        (key): key is string => typeof key === "string" && key in properties,
      )
    : [];
  return { ...schema, properties, required };
}

// nodeInitialValues adapts wire values to editor controls while preserving
// unknown asset keys for forward-compatible round trips.
function nodeInitialValues(node: BusinessNode): Record<string, unknown> {
  const values: Record<string, unknown> = {
    name: node.name,
    ref: node.ref,
    ...node.asset,
  };
  // AntD merges setFieldsValue payloads. Keys removed by canvas-level target
  // reconciliation must therefore be present as undefined, or the shared form
  // store resurrects the old reference when the node is inspected again.
  if (node.kind === "model") {
    values.model_id = node.asset.model_id;
    values.version = node.asset.version;
  }
  if (node.kind === "companion") {
    values.companion_id = node.asset.companion_id;
  }
  if (node.kind === "pipeline" && node.asset.definition !== undefined) {
    values.definition =
      typeof node.asset.definition === "string"
        ? node.asset.definition
        : JSON.stringify(node.asset.definition, null, 2);
  }
  if (node.kind === "northbound") {
    if (typeof node.asset.readings === "string") {
      values.readings = node.asset.readings
        .split(",")
        .map((reading) => reading.trim())
        .filter(Boolean);
    }
    if (typeof node.asset.edgex_resource_names === "string") {
      values.edgex_resource_names = node.asset.edgex_resource_names
        .split(",")
        .map((name) => name.trim())
        .filter(Boolean);
    }
  }
  if (node.kind === "camera" && typeof node.asset.publish_readings === "string") {
    values.publish_readings = node.asset.publish_readings
      .split(",")
      .map((reading) => reading.trim())
      .filter(Boolean);
  }
  if (node.kind === "external_servable") {
    let rawDefinition: unknown = node.asset.definition;
    if (typeof rawDefinition === "string" && rawDefinition.trim()) {
      try {
        rawDefinition = JSON.parse(rawDefinition) as unknown;
      } catch {
        rawDefinition = undefined;
      }
    }
    if (rawDefinition && typeof rawDefinition === "object" && !Array.isArray(rawDefinition)) {
      const definition = { ...(rawDefinition as Record<string, unknown>) };
      if (typeof definition.capabilities === "string") {
        definition.capabilities = definition.capabilities
          .split(",")
          .map((capability) => capability.trim())
          .filter(Boolean);
      }
      if (definition.descriptor !== undefined && typeof definition.descriptor !== "string") {
        definition.descriptor = JSON.stringify(definition.descriptor, null, 2);
      }
      values.definition = definition;
    }
  }
  if (node.kind === "action") {
    values.action_type = node.asset.action_type === "command"
      ? "send-command"
      : node.asset.action_type === "alarm"
        ? "create-alarm"
        : node.asset.action_type;
    if (!node.asset.resource && typeof node.asset.command_name === "string") {
      values.resource = node.asset.command_name;
    }
  }
  return values;
}

// deviceOptions presents registry names while retaining an imported or stale
// UUID for inspection. Retention never re-enables arbitrary manual entry.
function deviceOptions(devices: Device[], retainedId?: string) {
  const options = devices.map((device) => ({
    value: device.id,
    label: `${device.alias || device.canonical_name} (${device.id})`,
  }));
  if (retainedId && !options.some((option) => option.value === retainedId)) {
    options.unshift({ value: retainedId, label: retainedId });
  }
  return options;
}

// BusinessInspector renders global parameters and the selected asset form.
export default function BusinessInspector({
  node,
  assembly,
  parameterSchema,
  parameters,
  showParameters = true,
  onNodeChange,
  onParametersChange,
}: BusinessInspectorProps) {
  const { t } = useTranslation();
  const [assetForm] = Form.useForm();
  const globalSchema = flatParameterSchema(parameterSchema);
  const targetGatewayIds = useMemo(
    () => (node ? businessTargetGatewayIds(assembly, node.id) : []),
    [assembly, node],
  );

  // Canvas edits can reconcile an unselected asset and later reveal it, or
  // clear the currently selected asset after a relationship change. Sync those
  // external updates without remounting the form on every keystroke.
  useEffect(() => {
    if (node) assetForm.setFieldsValue(nodeInitialValues(node));
  }, [assetForm, node]);

  return (
    <aside
      aria-label={t("business.inspector.title")}
      style={{
        flex: "0 1 340px",
        width: 340,
        minWidth: 270,
        maxHeight: 680,
        overflow: "auto",
        padding: 16,
        border: "1px solid #f0f0f0",
        borderRadius: 8,
        background: "#fff",
      }}
    >
      {showParameters && (
        <>
          <Typography.Title level={5} style={{ marginTop: 0 }}>
            {t("business.inspector.parametersTitle")}
          </Typography.Title>
          {hasRenderableSchema(globalSchema) ? (
            <Form
              layout="vertical"
              initialValues={parameters}
              onValuesChange={(_, values: Record<string, unknown>) =>
                onParametersChange(values)
              }
            >
              <SchemaFields schema={globalSchema} />
            </Form>
          ) : (
            <Typography.Text type="secondary">
              {t("business.inspector.noParameters")}
            </Typography.Text>
          )}
          <Divider />
        </>
      )}
      <Typography.Title level={5}>{t("business.inspector.assetTitle")}</Typography.Title>
      {!node ? (
        <Typography.Text type="secondary">
          {t("business.inspector.selectNode")}
        </Typography.Text>
      ) : (
        <Form
          key={node.id}
          form={assetForm}
          layout="vertical"
          initialValues={nodeInitialValues(node)}
          onValuesChange={(changed: Record<string, unknown>, values: Record<string, unknown>) => {
            // AntD does not emit another onValuesChange for setFieldValue. Clear
            // dependent values in this same payload so stale references cannot
            // survive a gateway, model, target-device, or action-type change.
            const clearFields = (fields: string[]) => {
              for (const field of fields) {
                assetForm.setFieldValue(field, undefined);
                values[field] = undefined;
              }
            };
            if (node.kind === "camera" && "gateway_id" in changed) {
              clearFields(["device_id", "output_device_id"]);
            }
            if (node.kind === "model" && "model_id" in changed) {
              clearFields(["version"]);
            }
            if (node.kind === "action") {
              if ("action_type" in changed) {
                clearFields([
                  "target_device_id",
                  "resource",
                  "command_name",
                  "alarm_type",
                  "severity",
                ]);
              } else if ("target_device_id" in changed) {
                clearFields(["resource", "command_name"]);
              } else if ("resource" in changed) {
                // A retained legacy command_name has expansion precedence over
                // resource; clear it so the newly selected catalog row wins.
                clearFields(["command_name"]);
              }
            }
            const { name, ref, ...asset } = values;
            onNodeChange({
              ...node,
              name: String(name ?? ""),
              ref: String(ref ?? ""),
              asset,
            });
          }}
        >
          <Form.Item
            name="name"
            label={t("business.inspector.fields.name")}
            rules={[{ required: true, message: t("business.inspector.nameRequired") }]}
          >
            <Input />
          </Form.Item>
          <Form.Item name="ref" label={t("business.inspector.fields.ref")}>
            <Input placeholder={t("business.inspector.fields.refPlaceholder")} />
          </Form.Item>
          <AssetFields kind={node.kind} gatewayIds={targetGatewayIds} />
        </Form>
      )}
    </aside>
  );
}

// AssetFields selects the dedicated nested form for one asset kind.
function AssetFields({
  kind,
  gatewayIds,
}: {
  kind: BusinessNode["kind"];
  gatewayIds: string[];
}) {
  switch (kind) {
    case "camera":
      return <CameraFields />;
    case "pipeline":
      return <PipelineFields />;
    case "model":
      return <ModelFields gatewayIds={gatewayIds} />;
    case "companion":
      return <CompanionFields gatewayIds={gatewayIds} />;
    case "action":
      return <ActionFields />;
    case "northbound":
      return <NorthboundFields />;
    case "external_servable":
      return <ExternalServableFields />;
    case "rule":
      return <RuleFields />;
  }
}

// CameraFields edits binding references and scalar NI deployment settings;
// it does not expose technical pipeline nodes or frame-processing edges.
function CameraFields() {
  const { t } = useTranslation();
  const form = Form.useFormInstance();
  const gatewayId = Form.useWatch("gateway_id", form) as string | undefined;
  const deviceId = Form.useWatch("device_id", form) as string | undefined;
  const outputDeviceId = Form.useWatch("output_device_id", form) as string | undefined;
  const gatewaysQuery = useQuery({
    queryKey: ["gateway-devices"],
    queryFn: listGatewayDevices,
    refetchOnWindowFocus: false,
  });
  const camerasQuery = useQuery({
    queryKey: ["gateway-subdevices", gatewayId],
    queryFn: () => listDevicesByGateway(gatewayId as string),
    enabled: Boolean(gatewayId),
    refetchOnWindowFocus: false,
  });

  const gatewayOptions = (gatewaysQuery.data ?? []).map((device) => ({
    value: device.id,
    label: `${device.alias || device.canonical_name} (${device.id})`,
  }));
  if (gatewayId && !gatewayOptions.some((option) => option.value === gatewayId)) {
    gatewayOptions.unshift({ value: gatewayId, label: gatewayId });
  }
  const cameraOptions = deviceOptions(camerasQuery.data ?? [], deviceId);
  const outputOptions = deviceOptions(camerasQuery.data ?? [], outputDeviceId);
  const resourceError = gatewaysQuery.error ?? camerasQuery.error;

  return (
    <>
      {resourceError && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("business.inspector.resourceLoadFailed")}
          description={extractError(resourceError).message}
        />
      )}
      <Form.Item name="gateway_id" label={t("business.inspector.fields.gatewayId")}>
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          loading={gatewaysQuery.isFetching}
          options={gatewayOptions}
          placeholder={t("business.inspector.selectGateway")}
          notFoundContent={t("business.inspector.noGateways")}
        />
      </Form.Item>
      <Form.Item name="device_id" label={t("business.inspector.fields.deviceId")}>
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          disabled={!gatewayId}
          loading={camerasQuery.isFetching}
          options={cameraOptions}
          placeholder={t("business.inspector.selectCamera")}
          notFoundContent={t("business.inspector.noCameras")}
        />
      </Form.Item>
      <Form.Item name="output_device_id" label={t("business.inspector.fields.output")}>
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          disabled={!gatewayId}
          loading={camerasQuery.isFetching}
          options={outputOptions}
          placeholder={t("business.inspector.selectOutputDevice")}
          notFoundContent={t("business.inspector.noCameras")}
        />
      </Form.Item>
      <Form.Item name="source_kind" label={t("business.inspector.fields.sourceKind")}>
        <Select
          allowClear
          options={[
            { value: "onvif-camera", label: t("business.inspector.options.sourceOnvif") },
            { value: "rtsp", label: t("business.inspector.options.sourceRtsp") },
            { value: "jpeg", label: t("business.inspector.options.sourceJpeg") },
            { value: "none", label: t("business.inspector.options.sourceNone") },
          ]}
        />
      </Form.Item>
      <Form.Item name="source_device" label={t("business.inspector.fields.sourceDevice")}>
        <Input />
      </Form.Item>
      <Form.Item name="source_url" label={t("business.inspector.fields.sourceUrl")}>
        <Input />
      </Form.Item>
      <Form.Item name="secret_name" label={t("business.inspector.fields.secretName")}>
        <Input />
      </Form.Item>
      <Form.Item name="stream_profile" label={t("business.inspector.fields.streamProfile")}>
        <Input />
      </Form.Item>
      <Form.Item name="fps" label={t("business.inspector.fields.fps")}>
        <InputNumber min={0} step={0.1} style={{ width: "100%" }} />
      </Form.Item>
      <Form.Item name="decode_accel" label={t("business.inspector.fields.decodeAccel")}>
        <Select
          allowClear
          options={[
            { value: "software", label: t("business.inspector.options.decodeSoftware") },
            { value: "vaapi", label: t("business.inspector.options.decodeVaapi") },
          ]}
        />
      </Form.Item>
      <Form.Item name="vaapi_device" label={t("business.inspector.fields.vaapiDevice")}>
        <Input />
      </Form.Item>
      <Form.Item name="target_device" label={t("business.inspector.fields.targetDevice")}>
        <AutoComplete
          options={INFERENCE_TARGET_DEVICE_OPTIONS}
          placeholder={t("business.inspector.targetDevicePlaceholder")}
        />
      </Form.Item>
      <Form.Item name="publish_mode" label={t("business.inspector.fields.publishMode")}>
        <Select
          allowClear
          options={[
            { value: "all", label: t("business.inspector.options.publishAll") },
            { value: "nonempty", label: t("business.inspector.options.publishNonempty") },
            { value: "none", label: t("business.inspector.options.publishNone") },
          ]}
        />
      </Form.Item>
      <Form.Item
        name="publish_readings"
        label={t("business.inspector.fields.publishReadings")}
        extra={t("business.inspector.publishReadingsHint")}
      >
        <Select
          mode="tags"
          open={false}
          tokenSeparators={[","]}
          placeholder={t("business.inspector.publishReadingsPlaceholder")}
        />
      </Form.Item>
    </>
  );
}

// PipelineFields references a registered pipeline and optionally carries its
// opaque raw definition. NereidLink parses JSON only for transport safety and
// never interprets the definition as executable canvas content.
function PipelineFields() {
  const { t } = useTranslation();
  return (
    <>
      <Form.Item
        name="pipeline_id"
        label={t("business.inspector.fields.pipelineId")}
        extra={t("business.inspector.pipelineIdHint")}
      >
        <Input />
      </Form.Item>
      <Form.Item
        name="definition"
        label={t("business.inspector.fields.pipelineDefinition")}
        rules={[
          {
            validator: (_, value: unknown) => {
              if (typeof value !== "string" || !value.trim()) return Promise.resolve();
              try {
                const parsed = JSON.parse(value) as unknown;
                if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                  return Promise.resolve();
                }
              } catch {
                // The localized rejection below covers both malformed JSON and
                // valid JSON of the wrong top-level shape.
              }
              return Promise.reject(
                new Error(t("business.inspector.invalidDefinition")),
              );
            },
          },
        ]}
      >
        <Input.TextArea
          rows={7}
          placeholder={t("business.inspector.definitionPlaceholder")}
        />
      </Form.Item>
    </>
  );
}

// ModelFields offers only READY, non-stale id/version identities shared by
// every camera target gateway. Imported references remain visible for repair.
function ModelFields({ gatewayIds }: { gatewayIds: string[] }) {
  const { t } = useTranslation();
  const form = Form.useFormInstance();
  const modelId = Form.useWatch("model_id", form) as string | undefined;
  const version = Form.useWatch("version", form) as string | undefined;
  const modelsQuery = useQuery({
    queryKey: ["business-gateway-models", gatewayIds],
    queryFn: async () =>
      intersectReadyGatewayModels(
        await Promise.all(gatewayIds.map((gatewayId) => listGatewayModels(gatewayId))),
      ),
    enabled: gatewayIds.length > 0,
    refetchOnWindowFocus: false,
  });

  const models = modelsQuery.data ?? [];
  const modelOptions = [...new Map(models.map((model) => [model.id, model])).values()].map(
    (model) => ({
      value: model.id,
      label: model.model_name ? `${model.model_name} (${model.id})` : model.id,
    }),
  );
  if (modelId && !modelOptions.some((option) => option.value === modelId)) {
    modelOptions.unshift({ value: modelId, label: modelId });
  }
  const versionOptions = models
    .filter((model) => model.id === modelId)
    .map((model) => ({ value: model.version, label: model.version }));
  if (version && !versionOptions.some((option) => option.value === version)) {
    versionOptions.unshift({ value: version, label: version });
  }

  return (
    <>
      {modelsQuery.error && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("business.inspector.assetResourceLoadFailed")}
          description={extractError(modelsQuery.error).message}
        />
      )}
      <Form.Item
        name="model_id"
        label={t("business.inspector.fields.modelId")}
        extra={t("business.inspector.modelGatewayHint")}
      >
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          disabled={gatewayIds.length === 0}
          loading={modelsQuery.isFetching}
          options={modelOptions}
          placeholder={t("business.inspector.selectModel")}
          notFoundContent={t("business.inspector.noReadyModels")}
        />
      </Form.Item>
      <Form.Item name="version" label={t("business.inspector.fields.modelVersion")}>
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          disabled={!modelId}
          options={versionOptions}
          placeholder={t("business.inspector.selectModelVersion")}
          notFoundContent={t("business.inspector.noModelVersions")}
        />
      </Form.Item>
    </>
  );
}

// CompanionFields intersects enabled endpoint registrations across all target
// gateways and uses the device registry for conclusion-device references.
function CompanionFields({ gatewayIds }: { gatewayIds: string[] }) {
  const { t } = useTranslation();
  const form = Form.useFormInstance();
  const companionId = Form.useWatch("companion_id", form) as string | undefined;
  const conclusionDeviceId = Form.useWatch("conclusion_device_id", form) as string | undefined;
  const endpointsQuery = useQuery({
    queryKey: ["companion-endpoints"],
    queryFn: listCompanionEndpoints,
    refetchOnWindowFocus: false,
  });
  const devicesQuery = useQuery({
    queryKey: ["business-active-devices"],
    queryFn: async () =>
      (await listDevices({ state: "ACTIVE", page: 1, page_size: 500 })).items,
    refetchOnWindowFocus: false,
  });
  const companionOptions = availableCompanionIds(
    endpointsQuery.data ?? [],
    gatewayIds,
  ).map((value) => ({ value, label: value }));
  if (companionId && !companionOptions.some((option) => option.value === companionId)) {
    companionOptions.unshift({ value: companionId, label: companionId });
  }
  const conclusionOptions = deviceOptions(devicesQuery.data ?? [], conclusionDeviceId);
  const resourceError = endpointsQuery.error ?? devicesQuery.error;

  return (
    <>
      {resourceError && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("business.inspector.assetResourceLoadFailed")}
          description={extractError(resourceError).message}
        />
      )}
      <Form.Item
        name="companion_id"
        label={t("business.inspector.fields.companionId")}
        extra={t("business.inspector.companionGatewayHint")}
      >
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          disabled={gatewayIds.length === 0}
          loading={endpointsQuery.isFetching}
          options={companionOptions}
          placeholder={t("business.inspector.selectCompanion")}
          notFoundContent={t("business.inspector.noCompanions")}
        />
      </Form.Item>
      <Form.Item
        name="conclusion_device_id"
        label={t("business.inspector.fields.conclusionDeviceId")}
      >
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          loading={devicesQuery.isFetching}
          options={conclusionOptions}
          placeholder={t("business.inspector.selectConclusionDevice")}
          notFoundContent={t("business.inspector.noDevices")}
        />
      </Form.Item>
    </>
  );
}

// ActionFields uses closed action/severity values and conditionally exposes
// either a device-backed command catalog or alarm fields, never both at once.
function ActionFields() {
  const { t } = useTranslation();
  const form = Form.useFormInstance();
  const actionType = Form.useWatch("action_type", form) as string | undefined;
  const targetDeviceId = Form.useWatch("target_device_id", form) as string | undefined;
  const resource = Form.useWatch("resource", form) as string | undefined;
  const devicesQuery = useQuery({
    queryKey: ["business-active-devices"],
    queryFn: async () =>
      (await listDevices({ state: "ACTIVE", page: 1, page_size: 500 })).items,
    refetchOnWindowFocus: false,
  });
  const commandsQuery = useQuery({
    queryKey: ["business-command-catalog", targetDeviceId],
    queryFn: () => listCommandCatalog(targetDeviceId as string),
    enabled: actionType === "send-command" && Boolean(targetDeviceId),
    refetchOnWindowFocus: false,
  });
  const targetOptions = deviceOptions(devicesQuery.data ?? [], targetDeviceId);
  const commandOptions = (commandsQuery.data ?? [])
    .filter((entry) => entry.enabled)
    .map((entry) => ({
      value: entry.name,
      label: entry.description ? `${entry.name} — ${entry.description}` : entry.name,
    }));
  if (resource && !commandOptions.some((option) => option.value === resource)) {
    commandOptions.unshift({ value: resource, label: resource });
  }
  const resourceError = devicesQuery.error ?? commandsQuery.error;

  return (
    <>
      {resourceError && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("business.inspector.assetResourceLoadFailed")}
          description={extractError(resourceError).message}
        />
      )}
      <Form.Item name="action_type" label={t("business.inspector.fields.actionType")}>
        <Select
          allowClear
          placeholder={t("business.inspector.selectActionType")}
          options={[
            { value: "send-command", label: t("business.inspector.options.actionCommand") },
            { value: "create-alarm", label: t("business.inspector.options.actionAlarm") },
          ]}
        />
      </Form.Item>
      {actionType === "send-command" && (
        <>
          <Form.Item
            name="target_device_id"
            label={t("business.inspector.fields.actionTarget")}
          >
            <Select
              allowClear
              showSearch
              optionFilterProp="label"
              loading={devicesQuery.isFetching}
              options={targetOptions}
              placeholder={t("business.inspector.selectActionTarget")}
              notFoundContent={t("business.inspector.noDevices")}
            />
          </Form.Item>
          <Form.Item
            name="resource"
            label={t("business.inspector.fields.actionResource")}
            extra={t("business.inspector.commandResourceHint")}
          >
            <Select
              allowClear
              showSearch
              optionFilterProp="label"
              disabled={!targetDeviceId}
              loading={commandsQuery.isFetching}
              options={commandOptions}
              placeholder={t("business.inspector.selectCommand")}
              notFoundContent={t("business.inspector.noCommands")}
            />
          </Form.Item>
        </>
      )}
      {actionType === "create-alarm" && (
        <>
          <Form.Item name="alarm_type" label={t("business.inspector.fields.alarmType")}>
            <Input />
          </Form.Item>
          <Form.Item name="severity" label={t("business.inspector.fields.severity")}>
            <Select
              allowClear
              placeholder={t("business.inspector.selectSeverity")}
              options={[
                { value: "CRITICAL", label: t("status.critical") },
                { value: "MAJOR", label: t("status.major") },
                { value: "MINOR", label: t("status.minor") },
                { value: "WARNING", label: t("status.warning") },
              ]}
            />
          </Form.Item>
        </>
      )}
    </>
  );
}

// NorthboundFields requires an explicit forwarder/channel/reading whitelist and
// explicit raw EdgeX selectors. It offers neither wildcard nor embedding
// convenience controls because NE fail-closes those privacy-sensitive values.
function NorthboundFields() {
  const { t } = useTranslation();
  const form = Form.useFormInstance();
  const forwarderId = Form.useWatch("forwarder_id", form) as string | undefined;
  const forwardersQuery = useQuery({
    queryKey: ["forwarders"],
    queryFn: listForwarders,
    refetchOnWindowFocus: false,
  });
  const forwarderOptions = (forwardersQuery.data ?? [])
    .filter((forwarder) => forwarder.enabled)
    .map((forwarder) => ({
      value: forwarder.id,
      label: `${forwarder.name} (${forwarder.sink_type})`,
    }));
  if (forwarderId && !forwarderOptions.some((option) => option.value === forwarderId)) {
    forwarderOptions.unshift({ value: forwarderId, label: forwarderId });
  }

  return (
    <>
      {forwardersQuery.error && (
        <Alert
          type="error"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("business.inspector.assetResourceLoadFailed")}
          description={extractError(forwardersQuery.error).message}
        />
      )}
      <Form.Item name="forwarder_id" label={t("business.inspector.fields.forwarder")}>
        <Select
          allowClear
          showSearch
          optionFilterProp="label"
          loading={forwardersQuery.isFetching}
          options={forwarderOptions}
          placeholder={t("business.inspector.selectForwarder")}
          notFoundContent={t("business.inspector.noForwarders")}
        />
      </Form.Item>
      <Form.Item name="channel" label={t("business.inspector.fields.channel")}>
        <Input />
      </Form.Item>
      <Form.Item
        name="readings"
        label={t("business.inspector.fields.readings")}
        extra={t("business.inspector.readingsHint")}
      >
        <Select
          mode="tags"
          open={false}
          tokenSeparators={[","]}
          placeholder={t("business.inspector.readingsPlaceholder")}
        />
      </Form.Item>
      <Divider orientation="left" plain>
        {t("business.inspector.edgexSelectorTitle")}
      </Divider>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t("business.inspector.edgexSelectorHint")}
      />
      <Form.Item
        name="edgex_device_name"
        label={t("business.inspector.fields.edgexDeviceName")}
        extra={t("business.inspector.edgexDeviceNameHint")}
      >
        <Input />
      </Form.Item>
      <Form.Item
        name="edgex_profile_name"
        label={t("business.inspector.fields.edgexProfileName")}
        extra={t("business.inspector.edgexProfileNameHint")}
      >
        <Input />
      </Form.Item>
      <Form.Item
        name="edgex_resource_names"
        label={t("business.inspector.fields.edgexResourceNames")}
        extra={t("business.inspector.edgexResourceNamesHint")}
      >
        <Select
          mode="tags"
          open={false}
          tokenSeparators={[","]}
          placeholder={t("business.inspector.edgexResourceNamesPlaceholder")}
        />
      </Form.Item>
    </>
  );
}

// ExternalServableFields mirrors the external address-book fields. The
// TargetDevice input remains visible so imported drafts are inspectable and
// server validation can flag the forbidden selector instead of hiding it.
function ExternalServableFields() {
  const { t } = useTranslation();
  return (
    <>
      <Alert
        type="info"
        showIcon
        style={{ marginBottom: 16 }}
        message={t("business.inspector.externalTargetDeviceHint")}
      />
      <Form.Item name="servable_id" label={t("business.inspector.fields.servableId")}>
        <Input />
      </Form.Item>
      <Form.Item name={["definition", "family"]} label={t("business.inspector.fields.family")}>
        <Input />
      </Form.Item>
      <Form.Item name={["definition", "protocol"]} label={t("business.inspector.fields.protocol")}>
        <Input />
      </Form.Item>
      <Form.Item name={["definition", "endpoint"]} label={t("business.inspector.fields.endpoint")}>
        <Input />
      </Form.Item>
      <Form.Item name={["definition", "modelName"]} label={t("business.inspector.fields.modelName")}>
        <Input />
      </Form.Item>
      <Form.Item name={["definition", "modelVersion"]} label={t("business.inspector.fields.modelVersion")}>
        <Input />
      </Form.Item>
      <Form.Item name={["definition", "capabilities"]} label={t("business.inspector.fields.capabilities")}>
        <Select
          mode="tags"
          open={false}
          tokenSeparators={[","]}
          placeholder={t("business.inspector.capabilitiesPlaceholder")}
        />
      </Form.Item>
      <Form.Item
        name={["definition", "descriptor"]}
        label={t("business.inspector.fields.descriptor")}
        rules={[
          {
            validator: (_, value: unknown) => {
              if (typeof value !== "string" || !value.trim()) return Promise.resolve();
              try {
                const parsed = JSON.parse(value) as unknown;
                if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) {
                  return Promise.resolve();
                }
              } catch {
                // The same localized message handles malformed JSON and a
                // non-object JSON value.
              }
              return Promise.reject(
                new Error(t("business.inspector.invalidDescriptor")),
              );
            },
          },
        ]}
      >
        <Input.TextArea
          rows={4}
          placeholder={t("business.inspector.descriptorPlaceholder")}
        />
      </Form.Item>
      <Form.Item
        name={["definition", "TargetDevice"]}
        label={t("business.inspector.fields.targetDevice")}
        extra={t("business.inspector.externalTargetDeviceForbidden")}
      >
        <Input />
      </Form.Item>
    </>
  );
}

// RuleFields explains why only a reference is editable: the generated rule
// graph is declarative and its detailed composition is server-owned.
function RuleFields() {
  const { t } = useTranslation();
  return (
    <Typography.Paragraph type="secondary">
      {t("business.inspector.ruleReferenceHint")}
    </Typography.Paragraph>
  );
}
