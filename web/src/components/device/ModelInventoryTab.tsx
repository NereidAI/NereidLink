// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ModelInventoryTab shows a gateway's edge model inference surface
// (ADR-0039): the three independent status planes (runtime health from the
// device-shadow reported "inference" block, the manifest-reported model
// inventory, and the edge-inference connector bindings), plus a Plan-A
// binding wizard that orchestrates existing REST calls in one action:
// provision or reuse the virtual output sub-device and its display alias,
// then create or update the edge-inference connector bound to it, deleting
// a newly provisioned child device on failure. The kernel carries no inference semantics;
// only READY, non-stale models are bindable.

import { useEffect, useMemo, useState } from "react";
import {
  Alert,
  App as AntApp,
  AutoComplete,
  Button,
  Card,
  Col,
  Drawer,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Row,
  Segmented,
  Select,
  Space,
  Statistic,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import {
  isReadyForBinding,
  listGatewayModels,
  type GatewayModel,
} from "../../api/gatewayModels";
import {
  createConnector,
  deleteConnector,
  listConnectorSchemas,
  listConnectors,
  updateConnector,
  type ConnectorSpec,
} from "../../api/connectors";
import {
  createDevice,
  deleteDevice,
  getDevice,
  listDevices,
  listDevicesByGateway,
  reactivateDevice,
  updateDevice,
  type Device,
} from "../../api/devices";
import { invokeOperation } from "../../api/operationTasks";
import { listSecretBindings } from "../../api/secretBindings";
import { listProfiles } from "../../api/profiles";
import { extractError } from "../../api/client";
import { useWSStore } from "../../store/wsStore";
import {
  EDGE_INFERENCE_TYPE,
  findEdgeInferenceBinding,
  INFERENCE_TARGET_DEVICE_OPTIONS,
  inferenceOutputDeviceName,
  planEdgeInferenceBindingWrite,
  planInferenceDecodeConfig,
  VAAPI_DEVICE_PATTERN,
} from "./inferenceBinding";
import { useDeviceShadow } from "./useDeviceShadow";

interface RuntimeInfo {
  state: string;
  endpoint?: string;
  protocol?: string;
  modelsReady?: number;
  modelsTotal?: number;
  activeBindings?: number;
  latencyMs?: number;
  checkedAt?: string;
  probeIntervalS?: number;
  present: boolean;
}

function readRuntime(reported: Record<string, unknown> | undefined): RuntimeInfo {
  const raw = reported?.["inference"];
  if (!raw || typeof raw !== "object") return { state: "UNKNOWN", present: false };
  const b = raw as Record<string, unknown>;
  const num = (k: string): number | undefined =>
    typeof b[k] === "number" ? (b[k] as number) : undefined;
  return {
    present: true,
    state: typeof b["state"] === "string" ? (b["state"] as string) : "UNKNOWN",
    endpoint: typeof b["endpoint"] === "string" ? (b["endpoint"] as string) : undefined,
    protocol: typeof b["protocol"] === "string" ? (b["protocol"] as string) : undefined,
    modelsReady: num("models_ready"),
    modelsTotal: num("models_total"),
    activeBindings: num("active_bindings"),
    latencyMs: num("latency_ms"),
    checkedAt: typeof b["checked_at"] === "string" ? (b["checked_at"] as string) : undefined,
    probeIntervalS: num("probe_interval_s"),
  };
}

// TARGET_DEVICE_PATTERN validates the transport-safe operational deployment
// decision (protocol v1 §15.3), not model metadata. The shared preset list
// remains open to custom values through AutoComplete.
const TARGET_DEVICE_PATTERN = /^[A-Za-z0-9_.:,-]+$/;
const TARGET_DEVICE_MAX = 64;

// isRuntimeStale mirrors the cloud watchdog: silence past three probe
// intervals is treated as UNKNOWN/stale.
function runtimeState(rt: RuntimeInfo, now: number): string {
  if (!rt.present) return "";
  if (rt.state === "OFFLINE") return "OFFLINE";
  if (rt.checkedAt && rt.probeIntervalS && rt.probeIntervalS > 0) {
    const checked = Date.parse(rt.checkedAt);
    if (!Number.isNaN(checked) && now - checked > 3 * rt.probeIntervalS * 1000) {
      return "UNKNOWN";
    }
  }
  return rt.state;
}

function stateColor(state: string): string {
  switch (state) {
    case "READY":
    case "ONLINE":
    case "APPLIED":
      return "success";
    case "DEGRADED":
    case "EXTRACTING":
    case "VALIDATING":
    case "UNKNOWN":
      return "warning";
    case "FAILED":
    case "OFFLINE":
      return "error";
    default:
      return "default";
  }
}

interface WizardValues {
  source_device: string;
  source_mode: "snapshot" | "stream" | "url";
  source_profile: string;
  source_url: string;
  source_transport: "rtsp" | "rtsp-udp" | "mpjpeg" | "jpeg";
  source_secret: string;
  modelKey: string;
  output_alias: string;
  target_device: string;
  trigger: "event" | "periodic" | "continuous";
  period_ms: number;
  max_idle_ms: number;
  fps: number;
  error_threshold: number;
  conf_threshold: number;
  emit_frame: boolean;
  annotate_preview: boolean;
  ffmpeg_extra: string;
  decode_accel: "software" | "vaapi";
  vaapi_device: "" | "renderD129" | "renderD130" | "custom";
  vaapi_device_custom: string;
}

// FFMPEG_VALUE_FLAGS mirrors the NereidInference allowlist
// (internal/frame/ffmpeg.go validateExtraArgs): the authoritative check
// runs on the device service and rejects anything else (config-invalid);
// this client-side check is early feedback only.
const FFMPEG_VALUE_FLAGS = new Set([
  "-r",
  "-ss",
  "-t",
  "-threads",
  "-fflags",
  "-flags",
]);

function validateFFmpegExtra(input: string): boolean {
  const tokens = input.trim().split(/\s+/).filter(Boolean);
  for (let i = 0; i < tokens.length; i++) {
    const token = tokens[i];
    if (token === "-re") continue;
    if (FFMPEG_VALUE_FLAGS.has(token)) {
      const value = tokens[i + 1];
      if (!value || value.startsWith("-")) return false;
      i++;
      continue;
    }
    return false;
  }
  return true;
}

// CameraProfile is one ONVIF media profile as enumerated by the edge's
// read-only camera.profiles v1 operation (main/sub/third stream).
interface CameraProfile {
  token: string;
  name?: string;
  encoding?: string;
  width?: number;
  height?: number;
  fps?: number;
}

// DIRECT_TRANSPORTS mirrors the NE bridge schema enum for the mode=url
// escape hatch (non-ONVIF cameras). The bridge performs the authoritative
// validation; these checks only give immediate wizard feedback.
const DIRECT_TRANSPORTS = ["rtsp", "rtsp-udp", "mpjpeg", "jpeg"] as const;
type DirectTransport = (typeof DIRECT_TRANSPORTS)[number];

// validateDirectURL mirrors NereidEdge validateDirectURL: scheme must match
// the NI transport kind, a host is required, length <= 1024, and embedded
// userinfo (username/password in the URL) is rejected — credentials enter
// NI through an edge-provisioned named secret instead.
function validateDirectURL(raw: string, transport: DirectTransport): boolean {
  const value = raw.trim();
  if (value.length < 8 || value.length > 1024) return false;
  let u: URL;
  try {
    u = new URL(value);
  } catch {
    return false;
  }
  if (!u.hostname) return false;
  if (transport === "rtsp" || transport === "rtsp-udp") {
    if (u.protocol !== "rtsp:") return false;
  } else if (u.protocol !== "http:" && u.protocol !== "https:") {
    return false;
  }
  if (u.username || u.password) return false;
  return true;
}

interface Props {
  gatewayId: string;
  supported: boolean;
}

export default function ModelInventoryTab({ gatewayId, supported }: Props) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const modelsQ = useQuery({
    queryKey: ["gateway-models", gatewayId],
    queryFn: () => listGatewayModels(gatewayId),
  });
  const connectorsQ = useQuery({
    queryKey: ["gateway-connectors", gatewayId],
    queryFn: () => listConnectors(gatewayId),
  });
  const connectorSchemasQ = useQuery({
    queryKey: ["connector-schemas"],
    queryFn: listConnectorSchemas,
  });
  const managedQ = useQuery({
    queryKey: ["managed-devices", gatewayId],
    queryFn: () => listDevicesByGateway(gatewayId),
  });
  const profilesQ = useQuery({ queryKey: ["profiles"], queryFn: listProfiles });
  const shadowQ = useDeviceShadow(gatewayId, "gateway-model-inventory");

  const models = useMemo(
    () => (Array.isArray(modelsQ.data) ? modelsQ.data : []),
    [modelsQ.data],
  );
  const connectors = useMemo(
    () => (Array.isArray(connectorsQ.data) ? connectorsQ.data : []),
    [connectorsQ.data],
  );
  const managed = useMemo(
    () => (Array.isArray(managedQ.data) ? managedQ.data : []),
    [managedQ.data],
  );
  const profiles = useMemo(
    () => (Array.isArray(profilesQ.data) ? profilesQ.data : []),
    [profilesQ.data],
  );

  const bindings = useMemo(
    () => connectors.filter((c) => c.connector_type === EDGE_INFERENCE_TYPE),
    [connectors],
  );
  const bindable = useMemo(() => models.filter(isReadyForBinding), [models]);
  const detectionProfile = useMemo(
    () => profiles.find((p) => p.name === "detection/v1"),
    [profiles],
  );
  const [form] = Form.useForm<WizardValues>();
  const [wizardOpen, setWizardOpen] = useState(false);
  const [outputAliasEdited, setOutputAliasEdited] = useState(false);
  const trigger = Form.useWatch("trigger", form);
  const sourceMode = Form.useWatch("source_mode", form);
  const watchedSource = Form.useWatch("source_device", form);
  const watchedSourceUrl = Form.useWatch("source_url", form);
  const watchedSourceProfile = Form.useWatch("source_profile", form);
  const watchedSourceTransport = Form.useWatch("source_transport", form);
  const decodeAccel = Form.useWatch("decode_accel", form);
  const vaapiDevice = Form.useWatch("vaapi_device", form);
  const selectedModelKey = Form.useWatch("modelKey", form);
  const selectedModel: GatewayModel | undefined = useMemo(
    () => models.find((m) => `${m.id}@${m.version}` === selectedModelKey),
    [models, selectedModelKey],
  );

  // The edge-inference self-registered config schema is the authoritative
  // capability signal: the connector create call is validated against this
  // exact tenant catalog document, so properties/enum values missing here
  // would be rejected 400 by old firmware (source object also carries
  // additionalProperties:false). The wizard gates every post-Phase-A option
  // on it instead of assuming firmware capabilities.
  const edgeInferenceSchema = useMemo(() => {
    const schemas = Array.isArray(connectorSchemasQ.data)
      ? connectorSchemasQ.data
      : [];
    return schemas.find(
      (s) =>
        s.connector_type === EDGE_INFERENCE_TYPE &&
        (s.connector_type_version === "1" || !s.connector_type_version),
    );
  }, [connectorSchemasQ.data]);
  const targetDeviceSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | Record<string, unknown>
      | undefined;
    return !!props && Object.prototype.hasOwnProperty.call(props, "target_device");
  }, [edgeInferenceSchema]);
  const streamSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | { source?: { properties?: { mode?: { enum?: string[] } } } }
      | undefined;
    return (props?.source?.properties?.mode?.enum ?? []).includes("stream");
  }, [edgeInferenceSchema]);
  const urlSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | {
          source?: {
            properties?: {
              mode?: { enum?: string[] };
              url?: unknown;
              transport?: unknown;
            };
          };
        }
      | undefined;
    const p = props?.source?.properties;
    return (
      (p?.mode?.enum ?? []).includes("url") &&
      !!p?.url &&
      !!p?.transport
    );
  }, [edgeInferenceSchema]);
  const continuousSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | { trigger?: { enum?: string[] } }
      | undefined;
    return (props?.trigger?.enum ?? []).includes("continuous");
  }, [edgeInferenceSchema]);
  const profileSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | {
          source?: {
            properties?: {
              profile?: { type?: string; maxLength?: number };
            };
          };
        }
      | undefined;
    return !!props?.source?.properties?.profile;
  }, [edgeInferenceSchema]);
  const emitFrameSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | Record<string, unknown>
      | undefined;
    return !!props && Object.prototype.hasOwnProperty.call(props, "emit_frame");
  }, [edgeInferenceSchema]);
  const ffmpegExtraSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | Record<string, unknown>
      | undefined;
    return !!props && Object.prototype.hasOwnProperty.call(props, "ffmpeg_extra");
  }, [edgeInferenceSchema]);
  const annotateSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | Record<string, unknown>
      | undefined;
    return !!props && Object.prototype.hasOwnProperty.call(props, "annotate_preview");
  }, [edgeInferenceSchema]);
  const decodeAccelSupported = useMemo(() => {
    const props = edgeInferenceSchema?.config_schema?.properties as
      | Record<string, unknown>
      | undefined;
    return (
      !!props &&
      Object.prototype.hasOwnProperty.call(props, "decode_accel") &&
      Object.prototype.hasOwnProperty.call(props, "vaapi_device")
    );
  }, [edgeInferenceSchema]);
  const rtspDecodeSource =
    (sourceMode === "stream" && streamSupported) ||
    (sourceMode === "url" &&
      urlSupported &&
      (watchedSourceTransport === "rtsp" ||
        watchedSourceTransport === "rtsp-udp"));
  useEffect(() => {
    if (
      (!rtspDecodeSource || !decodeAccelSupported) &&
      form.getFieldValue("decode_accel") === "vaapi"
    ) {
      form.setFieldsValue({
        decode_accel: "software",
        vaapi_device: "",
        vaapi_device_custom: "",
      });
    }
  }, [decodeAccelSupported, form, rtspDecodeSource]);

  // The alias is display metadata only. Reuse an existing output device's
  // alias for an exact rebind; otherwise follow source/model changes until
  // the operator explicitly edits the suggested value.
  const suggestedOutputAlias = useMemo(() => {
    if (!selectedModel) return "";
    const directMode = urlSupported && sourceMode === "url";
    const source = directMode
      ? undefined
      : managed.find((d) => d.id === watchedSource);
    if (!directMode && !source) return "";
    const modelVariant = `${selectedModel.id}@${selectedModel.version}`;
    const existingBinding = findEdgeInferenceBinding(
      {
        source: directMode
          ? { mode: "url", url: watchedSourceUrl?.trim() ?? "" }
          : {
              device: source!.id,
              ...(sourceMode === "stream" &&
              streamSupported &&
              profileSupported &&
              watchedSourceProfile
                ? { profile: watchedSourceProfile }
                : {}),
            },
        model_ref: { id: selectedModel.id, version: selectedModel.version },
      },
      bindings,
    );
    if (existingBinding) {
      return (
        managed.find((d) => d.id === existingBinding.device_id)?.alias ?? ""
      );
    }
    if (directMode) {
      const value = watchedSourceUrl?.trim() ?? "";
      if (!value) return "";
      let host = value;
      try {
        host = new URL(value).host;
      } catch {
        // URL validation reports the field error; keep the suggestion useful.
      }
      return t("pages.models.wizard.childAliasUrl", {
        host,
        model: modelVariant,
      }).slice(0, 255);
    }
    return t("pages.models.wizard.childAlias", {
      source: source!.alias?.trim() || source!.canonical_name,
      model: modelVariant,
    }).slice(0, 255);
  }, [
    bindings,
    managed,
    profileSupported,
    selectedModel,
    sourceMode,
    streamSupported,
    t,
    urlSupported,
    watchedSource,
    watchedSourceProfile,
    watchedSourceUrl,
  ]);
  useEffect(() => {
    if (wizardOpen && !outputAliasEdited) {
      form.setFieldValue("output_alias", suggestedOutputAlias);
    }
  }, [form, outputAliasEdited, suggestedOutputAlias, wizardOpen]);

  // ONVIF media profiles (main/sub/third stream) are enumerated on demand
  // through the edge's read-only camera.profiles v1 gateway operation once
  // stream mode is chosen and both mode/profile capabilities are declared.
  // The selector stays a free-text AutoComplete, so a token the firmware
  // would not enumerate can still be typed and is sent verbatim.
  const profilesEnabled =
    !!watchedSource &&
    sourceMode === "stream" &&
    streamSupported &&
    profileSupported;
  const cameraProfilesQ = useQuery({
    queryKey: ["camera-profiles", gatewayId, watchedSource],
    enabled: profilesEnabled,
    retry: false,
    staleTime: 5 * 60 * 1000,
    queryFn: () =>
      invokeOperation(gatewayId, "camera.profiles", { source: watchedSource }),
  });
  const cameraProfiles = useMemo<CameraProfile[]>(() => {
    const result = cameraProfilesQ.data?.result as
      | { profiles?: CameraProfile[] }
      | undefined;
    return Array.isArray(result?.profiles) ? result?.profiles ?? [] : [];
  }, [cameraProfilesQ.data]);

  // Direct-URL bindings authenticate through an edge-side named secret.
  // Offer the gateway's cloud-delivered active secret bindings (ADR-0034)
  // as a filterable list; names provisioned straight into NI (never known
  // to the platform) can still be typed, so this stays an AutoComplete.
  const secretBindingsQ = useQuery({
    queryKey: ["gateway-secret-bindings", gatewayId],
    queryFn: () => listSecretBindings(gatewayId),
    enabled: sourceMode === "url" && urlSupported,
    staleTime: 30 * 1000,
  });
  const secretOptions = useMemo(
    () =>
      (secretBindingsQ.data ?? []).map((b) => ({
        value: b.secret_name,
        label: (
          <Space size={6}>
            <span>{b.secret_name}</span>
            <Tag style={{ marginInlineStart: 0 }}>{b.secret_type}</Tag>
            {b.status !== "APPLIED" && (
              <Tag color="warning" style={{ marginInlineStart: 0 }}>
                {t(`pages.deviceDetail.secrets.states.${b.status}`, {
                  defaultValue: b.status,
                })}
              </Tag>
            )}
          </Space>
        ),
      })),
    [secretBindingsQ.data, t],
  );
  const profileOptions = useMemo(
    () => [
      { value: "", label: t("pages.models.wizard.profileDefault") },
      ...cameraProfiles.map((p) => {
        const detail = [
          p.width && p.height ? `${p.width}×${p.height}` : "",
          p.encoding ?? "",
          p.fps ? `${p.fps}fps` : "",
        ]
          .filter(Boolean)
          .join(" · ");
        const head = p.name || p.token;
        return {
          value: p.token,
          label: detail ? `${head} (${detail})` : head,
        };
      }),
    ],
    [cameraProfiles, t],
  );

  const rtRaw = readRuntime(shadowQ.data?.reported);
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 5000);
    return () => clearInterval(id);
  }, []);
  const rtState = runtimeState(rtRaw, now);

  // Profile tokens belong to one camera: switching cameras clears the pick.
  useEffect(() => {
    form.setFieldValue("source_profile", "");
  }, [watchedSource, form]);

  // Live model-inventory / runtime events refresh the inventory and shadow.
  const subscribeWS = useWSStore((s) => s.subscribe);
  const unsubscribeWS = useWSStore((s) => s.unsubscribe);
  const modelEvents = useWSStore((s) => s.modelInventoryEvents);
  useEffect(() => {
    subscribeWS(`gateway-models-${gatewayId}`, "model_inventory", []);
    return () => unsubscribeWS(`gateway-models-${gatewayId}`);
  }, [gatewayId, subscribeWS, unsubscribeWS]);
  useEffect(() => {
    const last = modelEvents[modelEvents.length - 1];
    if (last?.gateway_id === gatewayId) {
      void Promise.all([
        queryClient.invalidateQueries({ queryKey: ["gateway-models", gatewayId] }),
        queryClient.invalidateQueries({ queryKey: ["device-shadow", gatewayId] }),
        queryClient.invalidateQueries({ queryKey: ["gateway-connectors", gatewayId] }),
      ]);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [modelEvents, gatewayId]);

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["gateway-models", gatewayId] }),
      queryClient.invalidateQueries({ queryKey: ["gateway-connectors", gatewayId] }),
      queryClient.invalidateQueries({ queryKey: ["managed-devices", gatewayId] }),
    ]);

  // Plan A orchestration: provision or reuse the virtual child and save its
  // display alias, then write the edge-inference connector; compensate by
  // deleting a newly provisioned child if the connector write fails.
  const bindMutation = useMutation({
    mutationFn: async (v: WizardValues) => {
      const model = models.find((m) => `${m.id}@${m.version}` === v.modelKey);
      const directMode = urlSupported && v.source_mode === "url";
      if (!model) throw new Error(t("pages.models.wizard.invalidSelection"));
      if (!detectionProfile) throw new Error(t("pages.models.wizard.noDetectionProfile"));
      // Cached queries drive display only. A binding write needs a consistent
      // fresh view so a loading/failed query cannot hide a legacy binding or
      // its existing child and cause a duplicate create.
      const [currentConnectors, currentManaged] = await Promise.all([
        listConnectors(gatewayId),
        listDevicesByGateway(gatewayId),
      ]);
      const currentBindings = currentConnectors.filter(
        (c) => c.connector_type === EDGE_INFERENCE_TYPE,
      );
      const source = directMode
        ? undefined
        : currentManaged.find((d) => d.id === v.source_device);
      if (!directMode && !source)
        throw new Error(t("pages.models.wizard.invalidSelection"));

      const localId = inferenceOutputDeviceName(
        directMode
          ? { directUrl: v.source_url }
          : {
              sourceCanonical: source!.canonical_name,
              sourceProfile:
                streamSupported &&
                profileSupported &&
                v.source_mode === "stream"
                  ? v.source_profile
                  : undefined,
            },
        model,
      );
      let outputAlias = (v.output_alias ?? "").trim();
      // Match an exact source+model-version binding semantically before
      // choosing the child. This finds devices named before model versions
      // were added to the canonical name, while another version on the same
      // stream still creates its own output device.
      const existingBinding = findEdgeInferenceBinding(
        {
          source: directMode
            ? { mode: "url", url: v.source_url.trim() }
            : {
                device: source!.id,
                ...(streamSupported &&
                profileSupported &&
                v.source_mode === "stream" &&
                v.source_profile
                  ? { profile: v.source_profile }
                  : {}),
              },
          model_ref: { id: model.id, version: model.version },
        },
        currentBindings,
      );
      // Unbind decommissions (never purges) the virtual output device. Resolve
      // an exact binding by id, or a retained device by its unique canonical
      // name, instead of assuming the first 500 managed rows are complete.
      let child: Device | undefined = currentManaged.find((d) =>
        existingBinding
          ? d.id === existingBinding.device_id
          : d.canonical_name === localId,
      );
      if (!child && existingBinding?.device_id) {
        try {
          child = await getDevice(existingBinding.device_id);
        } catch (err) {
          if (extractError(err).status !== 404) throw err;
        }
      }
      if (!child && !existingBinding) {
        const search = {
          managed_by: gatewayId,
          search: localId,
          page: 1,
          page_size: 500,
        };
        // The default state filter excludes decommissioned rows, so search
        // both views for the unique retained canonical name.
        const [visible, decommissioned] = await Promise.all([
          listDevices(search),
          listDevices({ ...search, state: "DECOMMISSIONED" }),
        ]);
        child = [...visible.items, ...decommissioned.items].find(
          (d) => d.canonical_name === localId,
        );
        // A pre-profile-aware binding retains no source metadata after unbind
        // deletes its connector. Only a decommissioned legacy-named device is
        // safe to reclaim: an active one may belong to another profile now.
        if (
          !child &&
          !directMode &&
          streamSupported &&
          profileSupported &&
          v.source_mode === "stream" &&
          v.source_profile.trim()
        ) {
          const legacyLocalId = inferenceOutputDeviceName(
            { sourceCanonical: source!.canonical_name },
            model,
          );
          const retained = await listDevices({
            managed_by: gatewayId,
            search: legacyLocalId,
            state: "DECOMMISSIONED",
            page: 1,
            page_size: 500,
          });
          child = retained.items.find(
            (d) => d.canonical_name === legacyLocalId,
          );
        }
      }
      let provisioned = false;
      if (child?.state === "DECOMMISSIONED") {
        child = await reactivateDevice(child.id);
        provisioned = true;
      }
      if (existingBinding && !child) {
        throw new Error(t("pages.models.wizard.outputDeviceMissing"));
      }
      const preserveOutputAlias = child !== undefined;
      if (!child) {
        child = await createDevice({
          canonical_name: localId,
          alias: outputAlias || undefined,
          profile_id: detectionProfile.id,
          gateway_id: gatewayId,
          state: "ACTIVE",
        });
        provisioned = true;
      }
      if (preserveOutputAlias && !outputAliasEdited) {
        outputAlias = child.alias ?? "";
      }
      try {
        if ((child.alias ?? "") !== outputAlias) {
          child = await updateDevice(child.id, {
            canonical_name: child.canonical_name,
            profile_id: child.profile_id,
            rule_chain_group_id: child.rule_chain_group_id,
            customer_id: child.customer_id,
            group_id: child.group_id,
            is_gateway: child.is_gateway ?? false,
            gateway_id: child.gateway_id ?? gatewayId,
            alias: outputAlias,
            notes: child.notes,
          });
        }
        const config: Record<string, unknown> = {
          // source.mode is omitted unless an advanced mode is both chosen
          // and advertised by the edge schema: the source object forbids
          // additional properties on old firmware, so an undeclared key
          // would 400 loudly rather than silently change acquisition.
          // mode=url addresses non-ONVIF cameras: no managed source device,
          // the direct URL (no embedded credentials) is sent instead.
          source: directMode
            ? {
                mode: "url",
                transport: v.source_transport,
                url: v.source_url.trim(),
                ...(v.source_secret.trim()
                  ? { secret_name: v.source_secret.trim() }
                  : {}),
              }
            : {
                device: source!.id,
                resource: "snapshot_jpeg",
                ...(streamSupported && v.source_mode === "stream"
                  ? { mode: "stream" }
                  : {}),
                // ProfileToken is stream-only and omitted unless the edge
                // schema declares it; absent means the camera default.
                ...(streamSupported &&
                profileSupported &&
                v.source_mode === "stream" &&
                v.source_profile
                  ? { profile: v.source_profile }
                  : {}),
              },
          model_ref: {
            id: model.id,
            version: model.version,
            ...(model.revision ? { revision: model.revision } : {}),
          },
          // Absent target_device is contractually equivalent to CPU, so the
          // default is omitted to stay wire-compatible with edge firmware
          // whose self-advertised schema predates this property. A non-CPU
          // choice is always sent; older firmware that forbids unknown
          // properties rejects it explicitly at create (no silent CPU run).
          ...(v.target_device && v.target_device !== "CPU"
            ? { target_device: v.target_device }
            : {}),
          trigger: v.trigger,
          error_threshold: v.error_threshold,
          conf_threshold: v.conf_threshold,
        };
        if (v.trigger === "periodic") config.period_ms = v.period_ms;
        if (v.trigger === "event") config.max_idle_ms = v.max_idle_ms;
        if (v.trigger === "continuous") config.fps = v.fps;
        // Frame/annotation defaults are true on the bridge, so like
        // target_device the explicit value is sent only when different
        // from default and only when the edge schema declares the
        // property (old firmware additionalProperties:false would 400).
        if (emitFrameSupported && v.emit_frame === false) {
          config.emit_frame = false;
        }
        if (annotateSupported && v.annotate_preview === false) {
          config.annotate_preview = false;
        }
        // ffmpeg tuning applies to ffmpeg-backed acquisition: the federated
        // ONVIF stream and every direct-URL transport kind.
        const extra = v.ffmpeg_extra.trim();
        const ffmpegMode =
          (v.source_mode === "stream" && streamSupported) ||
          (v.source_mode === "url" && urlSupported);
        if (ffmpegExtraSupported && ffmpegMode && extra) {
          config.ffmpeg_extra = extra;
        }
        const decode = planInferenceDecodeConfig({
          supported: decodeAccelSupported,
          rtspSource:
            (v.source_mode === "stream" && streamSupported) ||
            (v.source_mode === "url" &&
              urlSupported &&
              (v.source_transport === "rtsp" ||
                v.source_transport === "rtsp-udp")),
          decodeAccel: v.decode_accel,
          vaapiDevice:
            v.vaapi_device === "custom"
              ? v.vaapi_device_custom
              : v.vaapi_device,
        });
        if ("error" in decode) {
          throw new Error(t(`pages.models.wizard.${decode.error}`));
        }
        Object.assign(config, decode.config);
        const write = planEdgeInferenceBindingWrite(
          child.id,
          config,
          currentBindings,
        );
        if (write.kind === "update") {
          await updateConnector(gatewayId, write.specId, write.input);
        } else {
          await createConnector(gatewayId, write.input);
        }
      } catch (err) {
        // Compensating action: undo the create/reactivate performed above.
        // A pre-existing live device is left untouched.
        if (provisioned) {
          try {
            await deleteDevice(child.id);
          } catch {
            // best effort; surface the original error below
          }
        }
        throw err;
      }
    },
    onSuccess: async () => {
      await refresh();
      message.success(t("pages.models.wizard.bound"));
      setWizardOpen(false);
    },
    onError: (err) => message.error(extractError(err).message),
  });

  // Plan A unbind, the reverse of the bind wizard orchestration: delete the
  // connector first (the edge removes the NI job via the delete frame),
  // then decommission the virtual output sub-device. A failed child
  // decommission is surfaced as a warning instead of failing the unbind,
  // because the connector — the active binding — is already gone.
  const unbindMutation = useMutation({
    mutationFn: async (spec: ConnectorSpec) => {
      await deleteConnector(gatewayId, spec.spec_id);
      if (spec.device_id) {
        try {
          await deleteDevice(spec.device_id);
        } catch (err) {
          return { childError: extractError(err).message };
        }
      }
      return { childError: null as string | null };
    },
    onSuccess: async (res) => {
      await refresh();
      if (res.childError) {
        message.warning(
          t("pages.models.unboundChildFailed", { error: res.childError }),
        );
      } else {
        message.success(t("pages.models.unbound"));
      }
    },
    onError: (err) => message.error(extractError(err).message),
  });

  const readyCount = models.filter((m) => m.state === "READY" && !m.stale).length;
  const failedCount = models.filter((m) => m.state === "FAILED").length;
  const staleCount = models.filter((m) => m.stale).length;
  const bindingFailed = bindings.filter(
    (c) => c.state === "FAILED" || c.state === "DEGRADED",
  ).length;

  const sourceOptions = useMemo(
    () =>
      managed.map((d: Device) => ({
        value: d.id,
        label: d.alias ? `${d.canonical_name} (${d.alias})` : d.canonical_name,
      })),
    [managed],
  );
  const modelOptions = useMemo(
    () =>
      bindable.map((m) => ({
        value: `${m.id}@${m.version}`,
        label: `${m.id} ${m.version}${m.revision ? ` (${m.revision})` : ""} · ${m.backend}`,
      })),
    [bindable],
  );

  const modelColumns = [
    {
      title: t("pages.models.columns.model"),
      render: (_: unknown, m: GatewayModel) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{m.id}</Typography.Text>
          <Typography.Text type="secondary">
            {m.version}
            {m.revision ? ` · ${m.revision}` : ""}
            {m.model_name ? ` · ${m.model_name}` : ""}
          </Typography.Text>
        </Space>
      ),
    },
    {
      title: t("pages.models.columns.task"),
      render: (_: unknown, m: GatewayModel) => (
        <Space size={4}>
          <Tag>{m.task}</Tag>
          {m.family && <Tag>{m.family}</Tag>}
        </Space>
      ),
    },
    {
      title: t("pages.models.columns.modelServer"),
      render: (_: unknown, m: GatewayModel) => (
        <Space direction="vertical" size={0}>
          {/* Prefer the concrete server product once the edge reports it;
              older firmware only carries the protocol family in backend. */}
          <Tag style={{ marginInlineEnd: 0 }}>{m.server_family || m.backend}</Tag>
          {m.server_family && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {m.backend}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.models.columns.state"),
      render: (_: unknown, m: GatewayModel) => (
        <Tooltip title={m.reason || ""}>
          <Tag color={m.stale ? "warning" : stateColor(m.state)}>
            {m.stale ? t("pages.models.stale") : t(`pages.models.states.${m.state}`, { defaultValue: m.state })}
          </Tag>
        </Tooltip>
      ),
    },
    {
      title: t("pages.models.columns.labels"),
      render: (_: unknown, m: GatewayModel) => (
        <Space size={4} wrap>
          {(m.labels ?? []).slice(0, 8).map((l) => (
            <Tag key={l}>{l}</Tag>
          ))}
          {(m.labels?.length ?? 0) > 8 && <Tag>+{m.labels.length - 8}</Tag>}
        </Space>
      ),
    },
    {
      title: t("pages.models.columns.bindings"),
      dataIndex: "active_bindings",
      render: (n: number) => n ?? 0,
    },
  ];

  const bindingColumns = [
    {
      title: t("pages.models.columns.boundDevice"),
      dataIndex: "device_id",
      render: (deviceId?: string) => {
        const device = managed.find((d: Device) => d.id === deviceId);
        if (!deviceId) return "-";
        return (
          <Space direction="vertical" size={0}>
            <Link to={`/devices/${deviceId}`}>
              {device?.alias?.trim() || device?.canonical_name || deviceId}
            </Link>
            {device?.alias && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {device.canonical_name}
              </Typography.Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.models.columns.boundModel"),
      render: (_: unknown, c: ConnectorSpec) => {
        const raw = c.config?.model_ref;
        const model =
          raw && typeof raw === "object"
            ? (raw as Record<string, unknown>)
            : {};
        const id = typeof model.id === "string" ? model.id : "-";
        const version =
          typeof model.version === "string" ? model.version : "";
        const revision =
          typeof model.revision === "string" ||
          typeof model.revision === "number"
            ? String(model.revision)
            : "";
        return (
          <Space direction="vertical" size={2}>
            <Typography.Text strong>{id}</Typography.Text>
            {version && (
              <Tag color="processing" style={{ marginInlineEnd: 0 }}>
                {version}
              </Tag>
            )}
            {revision && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {t("pages.models.columns.modelRevision", { value: revision })}
              </Typography.Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.models.columns.acquisition"),
      render: (_: unknown, c: ConnectorSpec) => {
        const mode =
          c.config?.source &&
          typeof c.config.source === "object" &&
          "mode" in c.config.source &&
          typeof (c.config.source as Record<string, unknown>).mode === "string"
            ? ((c.config.source as Record<string, unknown>).mode as string)
            : "snapshot";
        const profile =
          c.config?.source &&
          typeof c.config.source === "object" &&
          "profile" in c.config.source &&
          typeof (c.config.source as Record<string, unknown>).profile === "string"
            ? ((c.config.source as Record<string, unknown>).profile as string)
            : "";
        const directURL =
          mode === "url" &&
          c.config?.source &&
          typeof c.config.source === "object" &&
          "url" in c.config.source &&
          typeof (c.config.source as Record<string, unknown>).url === "string"
            ? ((c.config.source as Record<string, unknown>).url as string)
            : "";
        const directTransport =
          mode === "url" &&
          c.config?.source &&
          typeof c.config.source === "object" &&
          "transport" in c.config.source &&
          typeof (c.config.source as Record<string, unknown>).transport === "string"
            ? ((c.config.source as Record<string, unknown>).transport as string)
            : "";
        const trig =
          typeof c.config?.trigger === "string"
            ? (c.config.trigger as string)
            : "event";
        const triggerKey = {
          event: "triggerEvent",
          periodic: "triggerPeriodic",
          continuous: "triggerContinuous",
        }[trig];
        return (
          <Space size={4} wrap>
            <Tag color={mode === "stream" ? "processing" : "default"}>
              {t(`pages.models.wizard.sourceMode.${mode}`, { defaultValue: mode })}
            </Tag>
            {mode === "stream" && profile && (
              <Tooltip title={t("pages.models.wizard.profile")}>
                <Tag>{profile}</Tag>
              </Tooltip>
            )}
            {mode === "url" && (
              <Tooltip title={directURL}>
                <Tag color="processing">
                  {directTransport ? `${directTransport} · ` : ""}
                  {(() => {
                    try {
                      return new URL(directURL).host;
                    } catch {
                      return directURL;
                    }
                  })()}
                </Tag>
              </Tooltip>
            )}
            <Tag>
              {triggerKey
                ? t(`pages.models.wizard.${triggerKey}`)
                : trig}
            </Tag>
          </Space>
        );
      },
    },
    {
      title: t("pages.models.columns.targetDevice"),
      render: (_: unknown, c: ConnectorSpec) => {
        const target =
          typeof c.config?.target_device === "string"
            ? (c.config.target_device as string)
            : "CPU";
        return <Tag>{target}</Tag>;
      },
    },
    {
      title: t("pages.models.columns.state"),
      render: (_: unknown, c: ConnectorSpec) => (
        <Tooltip title={c.report_reason || ""}>
          <Tag color={stateColor(c.state)}>
            {t(`pages.connectors.states.${c.state}`, { defaultValue: c.state })}
          </Tag>
        </Tooltip>
      ),
    },
    { title: t("pages.models.columns.reason"), dataIndex: "report_reason",
      render: (r?: string) => r || "-" },
    {
      title: t("pages.models.columns.action"),
      width: 110,
      render: (_: unknown, c: ConnectorSpec) => (
        <Popconfirm
          title={t("pages.models.unbindConfirm")}
          okText={t("pages.models.unbind")}
          cancelText={t("common.cancel")}
          okButtonProps={{ danger: true, loading: unbindMutation.isPending }}
          onConfirm={() => unbindMutation.mutate(c)}
        >
          <Button danger size="small" loading={unbindMutation.isPending}>
            {t("pages.models.unbind")}
          </Button>
        </Popconfirm>
      ),
    },
  ];

  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      {!supported && (
        <Alert
          type="info"
          showIcon
          message={t("pages.models.unsupported")}
        />
      )}
      <Row gutter={12}>
        <Col span={8}>
          <Card size="small">
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("pages.models.planes.runtime")}
            </Typography.Text>
            <div style={{ marginTop: 4 }}>
              {rtState ? (
                <Tag color={stateColor(rtState)} style={{ fontSize: 14, padding: "2px 10px" }}>
                  {t(`pages.models.runtime.${rtState}`, { defaultValue: rtState })}
                </Tag>
              ) : (
                <Typography.Text type="secondary">{t("common.none")}</Typography.Text>
              )}
            </div>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {rtRaw.present
                ? `${rtRaw.modelsReady ?? 0}/${rtRaw.modelsTotal ?? 0} · ${
                    rtRaw.activeBindings ?? 0
                  } ${t("pages.models.bindingsLabel")}${
                    rtRaw.latencyMs != null ? ` · ${rtRaw.latencyMs}ms` : ""
                  }`
                : t("pages.models.runtimeNoBlock")}
            </Typography.Text>
          </Card>
        </Col>
        <Col span={8}>
          <Card size="small">
            <Statistic
              title={t("pages.models.planes.ready")}
              value={readyCount}
              suffix={t("pages.models.planes.readySuffix", {
                total: models.length,
                failed: failedCount,
              })}
            />
            {staleCount > 0 && (
              <Tag color="warning">{t("pages.models.staleCount", { count: staleCount })}</Tag>
            )}
          </Card>
        </Col>
        <Col span={8}>
          <Card size="small">
            <Statistic
              title={t("pages.models.planes.bindings")}
              value={bindings.length}
              suffix={
                bindingFailed > 0
                  ? t("pages.models.planes.bindingsDegraded", { count: bindingFailed })
                  : ""
              }
            />
          </Card>
        </Col>
      </Row>

      <Card
        size="small"
        title={t("pages.models.inventoryTitle")}
        extra={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            disabled={bindable.length === 0}
            onClick={() => {
              // The connector-schemas query has no WebSocket invalidation and
              // refetchOnWindowFocus is disabled, so a long-lived console tab
              // can keep a pre-upgrade capability catalog until remount.
              // Refetch on every wizard open so the VA-API/target-device gates
              // reflect the edge firmware currently connected (see L-151).
              void connectorSchemasQ.refetch();
              setWizardOpen(true);
            }}
          >
            {t("pages.models.bindButton")}
          </Button>
        }
      >
        {bindable.length === 0 && (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 12 }}
            message={t("pages.models.noReadyModel")}
          />
        )}
        <Table
          rowKey={(m) => `${m.id}@${m.version}`}
          size="small"
          loading={modelsQ.isLoading}
          columns={modelColumns}
          dataSource={models}
          pagination={false}
          locale={{ emptyText: t("pages.models.empty") }}
        />
      </Card>

      <Card size="small" title={t("pages.models.bindingsTitle")}>
        <Table
          rowKey="spec_id"
          size="small"
          loading={connectorsQ.isLoading}
          columns={bindingColumns}
          dataSource={bindings}
          pagination={false}
          locale={{ emptyText: t("pages.models.bindingsEmpty") }}
        />
      </Card>

      <Drawer
        open={wizardOpen}
        title={t("pages.models.wizard.title")}
        placement="right"
        width={520}
        onClose={() => setWizardOpen(false)}
        destroyOnHidden
        afterOpenChange={(open) => {
          if (open) {
            form.resetFields();
            setOutputAliasEdited(false);
            form.setFieldValue("output_alias", suggestedOutputAlias);
          }
        }}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={() => setWizardOpen(false)}>{t("common.cancel")}</Button>
            <Button
              type="primary"
              loading={bindMutation.isPending}
              onClick={() => form.submit()}
            >
              {t("common.create")}
            </Button>
          </Space>
        }
      >
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 16 }}
          message={t("pages.models.wizard.intro")}
        />
        <Form
          form={form}
          layout="vertical"
          preserve={false}
          initialValues={{
            source_mode: "snapshot",
            source_profile: "",
            source_url: "",
            source_transport: "rtsp",
            source_secret: "",
            output_alias: "",
            target_device: "CPU",
            trigger: "event",
            period_ms: 1000,
            max_idle_ms: 60000,
            fps: 5,
            error_threshold: 5,
            conf_threshold: 0.25,
            emit_frame: true,
            annotate_preview: true,
            ffmpeg_extra: "",
            decode_accel: "software",
            vaapi_device: "",
            vaapi_device_custom: "",
          }}
          onFinish={(v) => bindMutation.mutate(v)}
        >
          {sourceMode !== "url" && (
            <Form.Item
              label={t("pages.models.wizard.source")}
              name="source_device"
              rules={[
                {
                  required: true,
                  message: t("pages.models.wizard.sourceRequired"),
                },
              ]}
            >
              <Select
                showSearch
                optionFilterProp="label"
                options={sourceOptions}
                placeholder={t("pages.models.wizard.sourceHint")}
              />
            </Form.Item>
          )}
          <Form.Item
            label={t("pages.models.wizard.sourceMode")}
            name="source_mode"
            tooltip={t("pages.models.wizard.sourceModeHint")}
            extra={
              streamSupported || urlSupported ? undefined : (
                <Alert
                  type="warning"
                  showIcon
                  style={{ marginTop: 6 }}
                  message={t("pages.models.wizard.sourceModeUnsupported")}
                />
              )
            }
          >
            <Segmented
              block
              disabled={!streamSupported && !urlSupported}
              options={[
                {
                  value: "snapshot",
                  label: t("pages.models.wizard.sourceMode.snapshot"),
                },
                {
                  value: "stream",
                  label: t("pages.models.wizard.sourceMode.stream"),
                  disabled: !streamSupported,
                },
                ...(urlSupported
                  ? ([
                      {
                        value: "url",
                        label: t("pages.models.wizard.sourceMode.url"),
                      },
                    ] as const)
                  : []),
              ]}
            />
          </Form.Item>
          {sourceMode === "url" && urlSupported && (
            <Alert
              type="warning"
              showIcon
              style={{ marginBottom: 16 }}
              message={t("pages.models.wizard.urlNote")}
              description={t("pages.models.wizard.urlNoteDetail")}
            />
          )}
          {sourceMode === "url" && urlSupported && (
            <>
              <Form.Item
                label={t("pages.models.wizard.transport")}
                name="source_transport"
              >
                <Select
                  options={DIRECT_TRANSPORTS.map((kind) => ({
                    value: kind,
                    label: t(`pages.models.wizard.transport_${kind}`),
                  }))}
                />
              </Form.Item>
              <Form.Item
                label={t("pages.models.wizard.sourceUrl")}
                name="source_url"
                tooltip={t("pages.models.wizard.sourceUrlHint")}
                rules={[
                  {
                    required: true,
                    message: t("pages.models.wizard.sourceUrlRequired"),
                  },
                  {
                    validator: (_: unknown, value: string) =>
                      !value ||
                      validateDirectURL(
                        value,
                        (form.getFieldValue(
                          "source_transport",
                        ) as DirectTransport) ?? "rtsp",
                      )
                        ? Promise.resolve()
                        : Promise.reject(
                            new Error(
                              t("pages.models.wizard.sourceUrlInvalid"),
                            ),
                          ),
                  },
                ]}
              >
                <Input
                  allowClear
                  autoComplete="off"
                  placeholder="rtsp://192.0.2.10:554/Streaming/Channels/101"
                />
              </Form.Item>
              <Form.Item
                label={t("pages.models.wizard.secretName")}
                name="source_secret"
                tooltip={t("pages.models.wizard.secretNameHint")}
                extra={
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t("pages.models.wizard.secretNameDropdown")}
                  </Typography.Text>
                }
                rules={[
                  {
                    max: 128,
                    pattern: /^[A-Za-z0-9_.:,-]*$/,
                    message: t("pages.models.wizard.secretNameInvalid"),
                  },
                ]}
              >
                <AutoComplete
                  allowClear
                  options={secretOptions}
                  style={{ width: "100%" }}
                  placeholder={t("pages.models.wizard.secretNamePlaceholder")}
                  filterOption={(input, option) =>
                    String(option?.value ?? "")
                      .toLowerCase()
                      .includes(input.toLowerCase())
                  }
                />
              </Form.Item>
            </>
          )}
          {sourceMode === "stream" && streamSupported && (
            <Alert
              type="info"
              showIcon
              style={{ marginBottom: 16 }}
              message={t("pages.models.wizard.streamNote")}
            />
          )}
          {sourceMode === "stream" && streamSupported && profileSupported && (
            <Form.Item
              label={t("pages.models.wizard.profile")}
              name="source_profile"
              tooltip={t("pages.models.wizard.profileHint")}
              extra={
                cameraProfilesQ.isError ? (
                  <Alert
                    type="warning"
                    showIcon
                    style={{ marginTop: 6 }}
                    message={t("pages.models.wizard.profileManual")}
                  />
                ) : cameraProfilesQ.isLoading ? (
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t("pages.models.wizard.profileLoading")}
                  </Typography.Text>
                ) : undefined
              }
            >
              <AutoComplete
                options={profileOptions}
                allowClear
                style={{ width: "100%" }}
                placeholder={t("pages.models.wizard.profileDefault")}
                filterOption={(input, option) =>
                  String(option?.label ?? "")
                    .toLowerCase()
                    .includes(input.toLowerCase())
                }
              />
            </Form.Item>
          )}
          <Form.Item
            label={t("pages.models.wizard.model")}
            name="modelKey"
            rules={[{ required: true, message: t("pages.models.wizard.modelRequired") }]}
          >
            <Select options={modelOptions} placeholder={t("pages.models.wizard.modelHint")} />
          </Form.Item>
          <Form.Item
            label={t("pages.models.wizard.outputAlias")}
            name="output_alias"
            tooltip={t("pages.models.wizard.outputAliasHint")}
            rules={[
              { max: 255, message: t("pages.devices.form.maxLength") },
            ]}
          >
            <Input
              allowClear
              maxLength={255}
              placeholder={t("pages.models.wizard.outputAliasPlaceholder")}
              onChange={() => setOutputAliasEdited(true)}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.models.wizard.targetDevice")}
            name="target_device"
            tooltip={t("pages.models.wizard.targetDeviceHint")}
            rules={[
              {
                required: true,
                message: t("pages.models.wizard.targetDeviceRequired"),
              },
              {
                validator: (_: unknown, value: string) =>
                  !!value &&
                  value.length <= TARGET_DEVICE_MAX &&
                  TARGET_DEVICE_PATTERN.test(value)
                    ? Promise.resolve()
                    : Promise.reject(
                        new Error(t("pages.models.wizard.targetDeviceInvalid")),
                      ),
              },
            ]}
            extra={
              targetDeviceSupported ? (
                <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                  {t("pages.models.wizard.targetDeviceSemantics")}
                </Typography.Text>
              ) : (
                <Alert
                  type="warning"
                  showIcon
                  style={{ marginTop: 6 }}
                  message={t("pages.models.wizard.targetDeviceUnsupported")}
                />
              )
            }
          >
            <AutoComplete
              disabled={!targetDeviceSupported}
              options={INFERENCE_TARGET_DEVICE_OPTIONS}
              placeholder="CPU"
              filterOption={(input, option) =>
                (option?.value ?? "")
                  .toLowerCase()
                  .includes(input.toLowerCase())
              }
            />
          </Form.Item>
          <Form.Item label={t("pages.models.wizard.trigger")} name="trigger">
            <Segmented
              block
              options={[
                { value: "event", label: t("pages.models.wizard.triggerEvent") },
                { value: "periodic", label: t("pages.models.wizard.triggerPeriodic") },
                ...(continuousSupported
                  ? [
                      {
                        value: "continuous" as const,
                        label: t("pages.models.wizard.triggerContinuous"),
                      },
                    ]
                  : []),
              ]}
            />
          </Form.Item>
          {trigger === "continuous" ? (
            <Form.Item
              label={t("pages.models.wizard.fps")}
              name="fps"
              rules={[
                {
                  validator: (_: unknown, value: number) =>
                    typeof value === "number" &&
                    Number.isFinite(value) &&
                    value > 0 &&
                    value <= 60
                      ? Promise.resolve()
                      : Promise.reject(new Error(t("pages.models.wizard.fpsInvalid"))),
                },
              ]}
            >
              <InputNumber min={0.1} max={60} step={0.5} addonAfter="FPS" style={{ width: "100%" }} />
            </Form.Item>
          ) : trigger === "periodic" ? (
            <Form.Item label={t("pages.models.wizard.periodMs")} name="period_ms">
              <InputNumber min={100} max={3600000} addonAfter="ms" style={{ width: "100%" }} />
            </Form.Item>
          ) : (
            <Form.Item label={t("pages.models.wizard.maxIdleMs")} name="max_idle_ms">
              <InputNumber min={1000} max={3600000} addonAfter="ms" style={{ width: "100%" }} />
            </Form.Item>
          )}
          <Form.Item label={t("pages.models.wizard.errorThreshold")} name="error_threshold">
            <InputNumber min={1} max={100} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item label={t("pages.models.wizard.confThreshold")} name="conf_threshold">
            <InputNumber min={0} max={1} step={0.05} style={{ width: "100%" }} />
          </Form.Item>
          {(emitFrameSupported ||
            annotateSupported ||
            ffmpegExtraSupported ||
            (decodeAccelSupported && rtspDecodeSource)) && (
            <Typography.Text
              type="secondary"
              strong
              style={{ display: "block", margin: "8px 0 4px", fontSize: 12 }}
            >
              {t("pages.models.wizard.advanced")}
            </Typography.Text>
          )}
          {emitFrameSupported && (
            <Form.Item
              label={t("pages.models.wizard.emitFrame")}
              name="emit_frame"
              tooltip={t("pages.models.wizard.emitFrameHint")}
              valuePropName="checked"
            >
              <Switch />
            </Form.Item>
          )}
          {annotateSupported && (
            <Form.Item
              label={t("pages.models.wizard.annotatePreview")}
              name="annotate_preview"
              tooltip={t("pages.models.wizard.annotatePreviewHint")}
              valuePropName="checked"
            >
              <Switch />
            </Form.Item>
          )}
          {decodeAccelSupported && rtspDecodeSource && (
            <>
              <Form.Item
                label={t("pages.models.wizard.decodeAccel")}
                name="decode_accel"
                tooltip={t("pages.models.wizard.decodeAccelHint")}
                extra={
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t("pages.models.wizard.decodeAccelNoFallback")}
                  </Typography.Text>
                }
              >
                <Select
                  options={[
                    {
                      value: "software",
                      label: t("pages.models.wizard.decodeAccelSoftware"),
                    },
                    {
                      value: "vaapi",
                      label: t("pages.models.wizard.decodeAccelVaapi"),
                    },
                  ]}
                />
              </Form.Item>
              {decodeAccel === "vaapi" && (
                <Form.Item
                  label={t("pages.models.wizard.vaapiDevice")}
                  name="vaapi_device"
                  tooltip={t("pages.models.wizard.vaapiDeviceHint")}
                >
                  <Select
                    options={[
                      {
                        value: "",
                        label: t("pages.models.wizard.vaapiDeviceDefault"),
                      },
                      { value: "renderD129", label: "renderD129" },
                      { value: "renderD130", label: "renderD130" },
                      {
                        value: "custom",
                        label: t("pages.models.wizard.vaapiDeviceCustom"),
                      },
                    ]}
                  />
                </Form.Item>
              )}
              {decodeAccel === "vaapi" && vaapiDevice === "custom" && (
                <Form.Item
                  label={t("pages.models.wizard.vaapiDeviceCustom")}
                  name="vaapi_device_custom"
                  rules={[
                    {
                      required: true,
                      message: t("pages.models.wizard.vaapiDeviceRequired"),
                    },
                    {
                      pattern: VAAPI_DEVICE_PATTERN,
                      message: t("pages.models.wizard.vaapiDeviceInvalid"),
                    },
                  ]}
                >
                  <Input
                    allowClear
                    autoComplete="off"
                    placeholder={t(
                      "pages.models.wizard.vaapiDeviceCustomPlaceholder",
                    )}
                  />
                </Form.Item>
              )}
            </>
          )}
          {ffmpegExtraSupported &&
            ((streamSupported && sourceMode === "stream") ||
              (urlSupported && sourceMode === "url")) && (
              <Form.Item
                label={t("pages.models.wizard.ffmpegExtra")}
                name="ffmpeg_extra"
                tooltip={t("pages.models.wizard.ffmpegExtraHint")}
                rules={[
                  {
                    validator: (_: unknown, value: string) =>
                      !value ||
                      (value.length <= 256 && validateFFmpegExtra(value))
                        ? Promise.resolve()
                        : Promise.reject(
                            new Error(t("pages.models.wizard.ffmpegExtraInvalid")),
                          ),
                  },
                ]}
                extra={
                  <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                    {t("pages.models.wizard.ffmpegExtraExample")}
                  </Typography.Text>
                }
              >
                <Input
                  allowClear
                  autoComplete="off"
                  placeholder="-fflags nobuffer -flags low_delay"
                />
              </Form.Item>
            )}
          {selectedModel && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {t("pages.models.wizard.modelRef", {
                id: selectedModel.id,
                version: selectedModel.version,
              })}
            </Typography.Text>
          )}
        </Form>
      </Drawer>
    </Space>
  );
}
