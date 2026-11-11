// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ConnectorsTab manages ADR-0026 edge connector desired specs for one gateway
// and shows the read-only desired/reported reconcile classification.

import { useEffect, useMemo, useState } from "react";
import {
  Alert,
  App as AntApp,
  AutoComplete,
  Button,
  Card,
  Divider,
  Drawer,
  Form,
  InputNumber,
  Popconfirm,
  Segmented,
  Select,
  Space,
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

import FilterSearchBar from "../FilterSearchBar";
import JsonEditor from "../connector/JsonConfigEditor";
import {
  isEdgeConnectorType,
  parseConfigObject,
  parseMappings,
} from "../connector/jsonEditor";
import { sanitizeEdgeName } from "../connector/protocolDict";

import {
  connectorTypeOptions,
  createConnector,
  deleteConnector,
  getConnectorReconcile,
  listConnectorSchemas,
  listConnectors,
  updateConnector,
  type ConnectorSaveInput,
  type ConnectorSpec,
} from "../../api/connectors";
import { listDevicesByGateway, type Device } from "../../api/devices";
import { getEdgeProfile } from "../../api/edgeProfiles";
import { listProfiles, type Profile } from "../../api/profiles";
import { extractError } from "../../api/client";
import { useWSStore } from "../../store/wsStore";
import { ConnectorStateColor as stateColor } from "../../utils/connectorState";

interface ConnectorsTabProps {
  // gatewayId is the gateway device being detailed.
  gatewayId: string;
}

// targetScope chooses whether a new connector is gateway-level or bound to
// one or more managed sub-devices (one spec per selected device).
type TargetScope = "gateway" | "devices";

interface EditorValues {
  connector_type: string;
  type_version: string;
  // scope drives create mode only; edit keeps the single bound device below.
  scope: TargetScope;
  device_ids?: string[];
  device_id?: string;
  profile_id?: string;
  configText: string;
  mappingsText: string;
  // Health-check editor fields.
  hc_enabled?: boolean;
  hc_resource?: string;
  hc_interval?: number;
  hc_timeout?: number;
  hc_threshold?: number;
}

// ConnectorsTab renders the connector table, reconcile banner and editor.
export default function ConnectorsTab({ gatewayId }: ConnectorsTabProps) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();

  const specsQuery = useQuery({
    queryKey: ["gateway-connectors", gatewayId],
    queryFn: () => listConnectors(gatewayId),
  });
  const reconcileQuery = useQuery({
    queryKey: ["gateway-connector-reconcile", gatewayId],
    queryFn: () => getConnectorReconcile(gatewayId),
  });
  const schemasQuery = useQuery({
    queryKey: ["connector-schemas"],
    queryFn: listConnectorSchemas,
  });
  const managedQuery = useQuery({
    queryKey: ["managed-devices", gatewayId],
    queryFn: () => listDevicesByGateway(gatewayId),
  });
  const profilesQuery = useQuery({
    queryKey: ["profiles"],
    queryFn: listProfiles,
  });

  // Memoized so the many derived useMemo hooks below keep stable
  // dependency identities instead of seeing fresh [] fallbacks.
  const specs = useMemo(
    () => (Array.isArray(specsQuery.data) ? specsQuery.data : []),
    [specsQuery.data],
  );
  const view = reconcileQuery.data;
  const schemas = useMemo(
    () => (Array.isArray(schemasQuery.data) ? schemasQuery.data : []),
    [schemasQuery.data],
  );
  const managed = useMemo(
    () => (Array.isArray(managedQuery.data) ? managedQuery.data : []),
    [managedQuery.data],
  );
  const profiles = useMemo(
    () => (Array.isArray(profilesQuery.data) ? profilesQuery.data : []),
    [profilesQuery.data],
  );

  const [modalOpen, setModalOpen] = useState(false);
  const [editing, setEditing] = useState<ConnectorSpec | undefined>();
  const [form] = Form.useForm<EditorValues>();

  // Client-side list filters. The list is a small unpaged per-gateway array,
  // so filtering stays local like the profiles page; the toolbar is the shared
  // right-aligned FilterSearchBar.
  const [keyword, setKeyword] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [stateFilter, setStateFilter] = useState("");
  const [reconcileFilter, setReconcileFilter] = useState("");
  const [bindingFilter, setBindingFilter] = useState("");
  const selectedType = Form.useWatch("connector_type", form);
  const selectedVersion = Form.useWatch("type_version", form);
  const selectedProfile = Form.useWatch("profile_id", form);
  const selectedDevice = Form.useWatch("device_id", form);
  const selectedDeviceIds: string[] = Form.useWatch("device_ids", form) ?? [];
  // scope only applies to create; the watch is harmless in edit (the field
  // is hidden and the single bound device is used).
  const targetScope = Form.useWatch("scope", form);

  // configSchema resolves the edge-registered schema of the chosen
  // type/version; an unversioned type falls back to its highest registration.
  const configSchema = useMemo(() => {
    if (!selectedType) return null;
    const exact = schemas.find(
      (s) =>
        s.connector_type === selectedType &&
        s.connector_type_version === selectedVersion,
    );
    if (exact) return exact.config_schema;
    const anyVersion = schemas
      .filter((s) => s.connector_type === selectedType)
      .sort((a, b) =>
        b.connector_type_version.localeCompare(a.connector_type_version),
      )[0];
    return anyVersion?.config_schema ?? null;
  }, [schemas, selectedType, selectedVersion]);

  // edgeDeviceNameOf resolves the EdgeX device_name for one sub-device: the
  // device's NL name (canonical_name, shown as "Name" across the console),
  // sanitized to EdgeX-legal characters (auto-provisioned names look like
  // "<gateway>/<local_id>" and contain '/', which EdgeX rejects). Returns
  // undefined when the device cannot be resolved.
  const edgeDeviceNameOf = (deviceId?: string): string | undefined => {
    if (!deviceId) return undefined;
    const canonical = managed.find(
      (device: Device) => device.id === deviceId,
    )?.canonical_name;
    return canonical ? sanitizeEdgeName(canonical) : undefined;
  };

  // deviceNameDefault backs the EdgeX device_name placeholder / fallback for
  // the single-device edit flow; multi-create resolves each name per device
  // on save instead.
  const deviceNameDefault = useMemo(
    () => edgeDeviceNameOf(selectedDevice),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [selectedDevice, managed],
  );

  // multiDeviceNames lists the EdgeX device_name that a bulk (multi/select-all)
  // create will inject per sub-device when config.device_name is left blank.
  // It is populated only for the create flow in device scope with multiple
  // targets; single-device edit uses deviceNameDefault.
  const multiDeviceNames = useMemo(() => {
    if (editing || targetScope !== "devices" || selectedDeviceIds.length < 2)
      return [];
    return selectedDeviceIds
      .map((id) => edgeDeviceNameOf(id))
      .filter((n): n is string => !!n);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [editing, targetScope, selectedDeviceIds, managed]);

  // selectedTemplate resolves the library profile bound in the editor; its
  // connector template feeds the live effective-config preview (ADR-0032
  // instance view: overrides only, merged result on the right).
  const selectedTemplate = useMemo(
    () => profiles.find((profile: Profile) => profile.id === selectedProfile),
    [profiles, selectedProfile],
  );
  const templateConfig = selectedTemplate?.connector_config_template ?? null;
  const templateMappingsList =
    selectedTemplate && Array.isArray(selectedTemplate.connector_mappings)
      ? (selectedTemplate.connector_mappings as unknown[])
      : null;

  // Point names from the bound profile (descriptor measurement_keys) suggest
  // EdgeX auto-event sourceNames and mapping keys; the editor unions them with
  // the gateway-reported EdgeX resources. Descriptors without a key are
  // ignored (ADR-0032: a resource without a measurement_key is not a point).
  const templatePointNames = useMemo(
    () =>
      Array.from(
        new Set(
          (selectedTemplate?.descriptors ?? [])
            .map((d) => d.measurement_key?.trim())
            .filter((k): k is string => !!k),
        ),
      ),
    [selectedTemplate],
  );

  // Health-check probe resources are picked from a real resource-name list:
  // the EdgeX profile's readable deviceResources when the connector type is
  // an edge family and the effective config names a profile, unioned with the
  // bound profile template's point keys. Same query keys as JsonConfigEditor
  // so the two panes share React Query cache.
  const configText = Form.useWatch("configText", form) as string | undefined;
  const isEdgeConnector = isEdgeConnectorType(selectedType);
  const effectiveEdgeProfileName = useMemo(() => {
    try {
      const override = JSON.parse(configText || "{}") as Record<
        string,
        unknown
      >;
      const fromOverride = override.profile_name;
      if (typeof fromOverride === "string" && fromOverride.trim()) {
        return fromOverride.trim();
      }
    } catch {
      // Invalid JSON in the textarea: fall through to the template name.
    }
    const fromTemplate = (templateConfig as Record<string, unknown> | null)
      ?.profile_name;
    return typeof fromTemplate === "string" ? fromTemplate : "";
  }, [configText, templateConfig]);
  const edgeProfileForHealthQ = useQuery({
    queryKey: ["gateway-edge-profile", gatewayId, effectiveEdgeProfileName],
    queryFn: () => getEdgeProfile(gatewayId, effectiveEdgeProfileName),
    enabled: isEdgeConnector && !!effectiveEdgeProfileName,
  });
  const healthResourceOptions = useMemo(() => {
    const edgeNames =
      edgeProfileForHealthQ.data?.resources
        .filter((resource) => resource.translatable)
        .map((resource) => resource.name) ?? [];
    return Array.from(new Set([...edgeNames, ...templatePointNames])).map(
      (value) => ({
        value,
        label: value,
      }),
    );
  }, [edgeProfileForHealthQ.data, templatePointNames]);

  // classOf maps a spec id to its reconcile class from the latest view. The
  // reconcile slices are defaulted defensively: an older/partial backend or a
  // rejected response must never crash the tab on `.forEach` (white screen).
  const classOf = useMemo(() => {
    const m = new Map<string, "in_sync" | "missing" | "stale" | "drifted">();
    (view?.in_sync ?? []).forEach((id) => m.set(id, "in_sync"));
    (view?.missing ?? []).forEach((id) => m.set(id, "missing"));
    (view?.stale ?? []).forEach((id) => m.set(id, "stale"));
    (view?.drifted ?? []).forEach((id) => m.set(id, "drifted"));
    return m;
  }, [view]);

  const reconcile = {
    inSync: view?.in_sync ?? [],
    missing: view?.missing ?? [],
    stale: view?.stale ?? [],
    drifted: view?.drifted ?? [],
    extra: view?.extra ?? [],
  };

  const invalidate = () =>
    Promise.all([
      queryClient.invalidateQueries({
        queryKey: ["gateway-connectors", gatewayId],
      }),
      queryClient.invalidateQueries({
        queryKey: ["gateway-connector-reconcile", gatewayId],
      }),
    ]);

  // Live updates: subscribe to tenant connector lifecycle events. A frame
  // naming this gateway refreshes both the list and the reconcile view; the
  // rolling buffer is only used as a trigger, never as the data source. The
  // subscription is tenant-wide (devices: []) because a connector event is a
  // per-gateway change notification, not device telemetry.
  const subscribeWS = useWSStore((state) => state.subscribe);
  const unsubscribeWS = useWSStore((state) => state.unsubscribe);
  const connectorEvents = useWSStore((state) => state.connectorEvents);

  useEffect(() => {
    subscribeWS(`gateway-connectors-${gatewayId}`, "connector", []);
    return () => unsubscribeWS(`gateway-connectors-${gatewayId}`);
  }, [gatewayId, subscribeWS, unsubscribeWS]);

  useEffect(() => {
    if (connectorEvents.length === 0) return;
    const latest = connectorEvents[connectorEvents.length - 1];
    if (latest.gateway_id === gatewayId) {
      void invalidate();
    }
    // Only the arrival of a new event should trigger a refresh.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connectorEvents, gatewayId]);

  const saveMutation = useMutation({
    mutationFn: async (values: EditorValues) => {
      const configResult = parseConfigObject(values.configText);
      if (!configResult.ok || !configResult.value) {
        throw new Error(
          `${t("pages.connectors.errors.configJson")}: ${configResult.error ?? ""}`,
        );
      }
      const mappingsResult = parseMappings(values.mappingsText);
      if (!mappingsResult.ok || !mappingsResult.value) {
        throw new Error(
          `${t("pages.connectors.errors.mappingsJson")}: ${mappingsResult.error ?? ""}`,
        );
      }
      const mappings = mappingsResult.value;
      if (values.hc_enabled) {
        const interval = values.hc_interval ?? 30000;
        const timeout = values.hc_timeout ?? 5000;
        if (timeout >= interval) {
          throw new Error(t("pages.connectors.errors.healthCheckTiming"));
        }
      }
      const healthCheck = values.hc_enabled
        ? {
            resource: (values.hc_resource ?? "").trim(),
            interval_ms: values.hc_interval ?? 30000,
            timeout_ms: values.hc_timeout ?? 5000,
            fail_threshold: values.hc_threshold ?? 3,
          }
        : undefined;
      const base: ConnectorSaveInput = {
        connector_type: values.connector_type,
        type_version: values.type_version || undefined,
        profile_id: values.profile_id || undefined,
        mappings: mappings.length ? mappings : undefined,
        health_check: healthCheck,
      };

      // EdgeX connectors require config.device_name; when left blank it is
      // injected per bound sub-device from its NL name (canonical_name,
      // sanitized to EdgeX-legal chars), so a bulk create on many devices
      // gives each connector its own device_name and an unpersonalized device
      // needs no explicit name.
      const configWithDeviceName = (
        deviceId?: string,
      ): Record<string, unknown> | undefined => {
        const config = { ...configResult.value! };
        if (
          isEdgeConnectorType(values.connector_type) &&
          !String(config.device_name ?? "").trim()
        ) {
          const edgeName = edgeDeviceNameOf(deviceId);
          if (edgeName) config.device_name = edgeName;
        }
        return Object.keys(config).length ? config : undefined;
      };

      if (editing) {
        // Edit stays a single spec. Its binding is taken from the form: scope
        // "gateway" clears device_id, scope "devices" binds the one selected
        // sub-device. Switching a bound device on save is allowed.
        const deviceId =
          values.scope === "devices" && values.device_id
            ? values.device_id
            : undefined;
        const input: ConnectorSaveInput = {
          ...base,
          config: configWithDeviceName(deviceId),
          device_id: deviceId,
        };
        await updateConnector(gatewayId, editing.spec_id, input);
        return { created: 1, failed: 0, failures: [] as string[] };
      }
      // Create: gateway-level makes one spec; device scope makes one spec per
      // selected sub-device. Each selected device is created independently so
      // one rejection (e.g. already bound) does not abort the rest.
      const targets =
        values.scope === "devices" ? (values.device_ids ?? []) : [undefined];
      const results = await Promise.allSettled(
        targets.map((deviceId) =>
          createConnector(gatewayId, {
            ...base,
            config: configWithDeviceName(deviceId ?? undefined),
            device_id: deviceId ?? undefined,
          }),
        ),
      );
      let created = 0;
      const failures: string[] = [];
      results.forEach((result) => {
        if (result.status === "fulfilled") {
          created += 1;
        } else {
          failures.push(extractError(result.reason).message);
        }
      });
      return { created, failed: failures.length, failures };
    },
    onSuccess: async (summary) => {
      await invalidate();
      if (summary.failed === 0) {
        message.success(t("pages.connectors.saved"));
        setModalOpen(false);
        return;
      }
      if (summary.created > 0) {
        message.warning(
          t("pages.connectors.bulkPartial", {
            created: summary.created,
            failed: summary.failed,
            reason: summary.failures[0] ?? "",
          }),
        );
        setModalOpen(false);
      } else {
        message.error(
          t("pages.connectors.bulkFailed", {
            failed: summary.failed,
            reason: summary.failures[0] ?? "",
          }),
        );
      }
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: (specId: string) => deleteConnector(gatewayId, specId),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.connectors.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const typeOptions = useMemo(() => connectorTypeOptions(schemas), [schemas]);

  // boundDeviceIds flags sub-devices that already own a connector; the unique
  // (tenant, device) index rejects a second instance, so they are offered
  // disabled in the multi-select rather than failing server-side.
  const boundDeviceIds = useMemo(
    () =>
      new Set(
        specs.map((spec) => spec.device_id).filter((id): id is string => !!id),
      ),
    [specs],
  );

  // selectableManaged lists sub-devices that may still receive an instance.
  const selectableManaged = useMemo(
    () => managed.filter((device: Device) => !boundDeviceIds.has(device.id)),
    [managed, boundDeviceIds],
  );

  const deviceOptions = useMemo(
    () =>
      managed.map((device: Device) => ({
        value: device.id,
        label: boundDeviceIds.has(device.id)
          ? `${device.canonical_name} (${t("pages.connectors.alreadyBound")})`
          : device.canonical_name,
        disabled: boundDeviceIds.has(device.id),
      })),
    [managed, boundDeviceIds, t],
  );

  // editDeviceOptions backs the single-device picker in edit mode. The
  // currently bound device is always included even while the managed-device
  // list is still loading, otherwise the Select first renders a value with
  // no matching option, caches an empty label and keeps the box blank after
  // the options arrive.
  const editDeviceOptions = useMemo(() => {
    const options = managed.map((device: Device) => ({
      value: device.id,
      label:
        boundDeviceIds.has(device.id) && device.id !== editing?.device_id
          ? `${device.canonical_name} (${t("pages.connectors.alreadyBound")})`
          : device.canonical_name,
      disabled:
        boundDeviceIds.has(device.id) && device.id !== editing?.device_id,
    }));
    if (
      editing?.device_id &&
      !managed.some((device: Device) => device.id === editing.device_id)
    ) {
      options.unshift({
        value: editing.device_id,
        label: `${editing.device_id} (${t("common.loading")})`,
        disabled: false,
      });
    }
    return options;
  }, [managed, boundDeviceIds, editing, t]);

  // versionOptions follows the currently selected connector type.
  const versionOptions = useMemo(
    () =>
      schemas
        .filter(
          (schema) =>
            schema.connector_type === selectedType &&
            schema.connector_type_version,
        )
        .map((schema) => ({ value: schema.connector_type_version })),
    [schemas, selectedType],
  );

  const openCreate = () => {
    setEditing(undefined);
    setModalOpen(true);
  };
  const openEdit = (spec: ConnectorSpec) => {
    setEditing(spec);
    setModalOpen(true);
  };

  const driftCount =
    reconcile.missing.length +
    reconcile.stale.length +
    reconcile.drifted.length +
    reconcile.extra.length;

  // nameOf resolves a bound device id to its canonical name for searching and
  // for the reconcile-independent table label.
  const nameOf = (deviceId?: string) =>
    deviceId
      ? (managed.find((device: Device) => device.id === deviceId)
          ?.canonical_name ?? deviceId)
      : "";

  // effectiveProfileOf resolves the device profile behind one connector:
  // the profile template bound on the spec, falling back to the bound
  // sub-device's profile when no template id is recorded. Returns undefined
  // when neither resolves.
  const effectiveProfileOf = (spec: ConnectorSpec): Profile | undefined => {
    const profileID =
      spec.profile_id ??
      (spec.device_id
        ? managed.find((device: Device) => device.id === spec.device_id)
            ?.profile_id
        : undefined);
    if (!profileID) return undefined;
    return profiles.find((profile: Profile) => profile.id === profileID);
  };

  const typeFilterOptions = useMemo(
    () =>
      Array.from(new Set(specs.map((spec) => spec.connector_type))).map(
        (value) => ({
          value,
          label: value,
        }),
      ),
    [specs],
  );

  const stateFilterOptions = useMemo(() => {
    const known = [
      "PENDING",
      "DISPATCHED",
      "CREATING",
      "APPLIED",
      "DEGRADED",
      "FAILED",
    ];
    return known
      .filter((state) => specs.some((spec) => spec.state === state))
      .map((state) => ({
        value: state,
        label: t(`pages.connectors.states.${state}`, { defaultValue: state }),
      }));
  }, [specs, t]);

  const reconcileFilterOptions = [
    { value: "in_sync", label: t("pages.connectors.classes.in_sync") },
    { value: "missing", label: t("pages.connectors.classes.missing") },
    { value: "stale", label: t("pages.connectors.classes.stale") },
    { value: "drifted", label: t("pages.connectors.classes.drifted") },
    { value: "unknown", label: t("pages.connectors.classes.unknown") },
  ];

  const filteredSpecs = useMemo(() => {
    const needle = keyword.trim().toLowerCase();
    return specs.filter((spec) => {
      if (typeFilter && spec.connector_type !== typeFilter) return false;
      if (stateFilter && spec.state !== stateFilter) return false;
      if (bindingFilter === "gateway" && spec.device_id) return false;
      if (bindingFilter === "device" && !spec.device_id) return false;
      if (reconcileFilter) {
        // Mirror the column: a class only counts when a watermark exists,
        // otherwise the row reads as "unknown".
        const cls = classOf.get(spec.spec_id);
        const displayed = cls && view?.watermark ? cls : "unknown";
        if (displayed !== reconcileFilter) return false;
      }
      if (needle) {
        const profile = effectiveProfileOf(spec);
        const haystack =
          `${spec.connector_type} ${spec.type_version} ${nameOf(spec.device_id)} ` +
          `${profile?.name ?? ""} ${profile?.protocol ?? ""}`.toLowerCase();
        if (!haystack.includes(needle)) return false;
      }
      return true;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    specs,
    keyword,
    typeFilter,
    stateFilter,
    reconcileFilter,
    bindingFilter,
    classOf,
    view,
    managed,
    profiles,
  ]);

  const columns = [
    {
      title: t("pages.connectors.columns.type"),
      dataIndex: "connector_type",
      render: (type: string, spec: ConnectorSpec) => (
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{type}</Typography.Text>
          {spec.type_version && (
            <Typography.Text type="secondary">
              {spec.type_version}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.connectors.columns.profile"),
      render: (_: unknown, spec: ConnectorSpec) => {
        const profile = effectiveProfileOf(spec);
        if (!profile) {
          return <Typography.Text type="secondary">-</Typography.Text>;
        }
        return (
          <Space direction="vertical" size={0}>
            <Link to={`/profiles/${profile.id}`}>{profile.name}</Link>
            {profile.protocol && (
              <Typography.Text type="secondary">
                {profile.protocol}
              </Typography.Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.connectors.columns.boundDevice"),
      dataIndex: "device_id",
      render: (deviceId?: string) => {
        if (!deviceId) {
          return <Tag>{t("pages.connectors.gatewayLevel")}</Tag>;
        }
        const bound = managed.find(
          (device: Device) => device.id === deviceId,
        );
        return (
          <Space direction="vertical" size={0}>
            <Link to={`/devices/${deviceId}`}>
              {bound?.canonical_name ?? deviceId}
            </Link>
            {bound?.alias && (
              <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                {bound.alias}
              </Typography.Text>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.connectors.columns.state"),
      dataIndex: "state",
      render: (state: string, spec: ConnectorSpec) => (
        <Tooltip title={spec.report_reason || ""}>
          <Tag color={stateColor[state] ?? "default"}>
            {t(`pages.connectors.states.${state}`, { defaultValue: state })}
          </Tag>
        </Tooltip>
      ),
    },
    {
      title: t("pages.connectors.columns.reconcile"),
      render: (_: unknown, spec: ConnectorSpec) => {
        const cls = classOf.get(spec.spec_id);
        if (!cls || !view?.watermark) {
          return (
            <Typography.Text type="secondary">
              {t("pages.connectors.classes.unknown")}
            </Typography.Text>
          );
        }
        const color = {
          in_sync: "success",
          missing: "error",
          stale: "warning",
          drifted: "warning",
        }[cls];
        return <Tag color={color}>{t(`pages.connectors.classes.${cls}`)}</Tag>;
      },
    },
    {
      title: t("pages.connectors.columns.revision"),
      render: (_: unknown, spec: ConnectorSpec) =>
        `${spec.revision} / ${spec.reported_revision}`,
    },
    {
      title: t("pages.connectors.columns.actions"),
      render: (_: unknown, spec: ConnectorSpec) => (
        <Space>
          <Button size="small" onClick={() => openEdit(spec)}>
            {t("common.edit")}
          </Button>
          <Popconfirm
            title={t("pages.connectors.deleteConfirm")}
            okText={t("common.delete")}
            cancelText={t("common.cancel")}
            okButtonProps={{ danger: true }}
            onConfirm={() => deleteMutation.mutate(spec.spec_id)}
          >
            <Button size="small" danger>
              {t("common.delete")}
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <Card
      extra={
        <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
          {t("pages.connectors.add")}
        </Button>
      }
    >
      {!view?.watermark && (
        <Alert
          type="info"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.connectors.noManifest")}
        />
      )}
      {view?.watermark && driftCount > 0 && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={
            <Space wrap>
              <span>{t("pages.connectors.driftSummary")}</span>
              {reconcile.missing.length > 0 && (
                <Tag color="error">
                  {t("pages.connectors.classes.missing")}:{" "}
                  {reconcile.missing.length}
                </Tag>
              )}
              {reconcile.stale.length > 0 && (
                <Tag color="warning">
                  {t("pages.connectors.classes.stale")}:{" "}
                  {reconcile.stale.length}
                </Tag>
              )}
              {reconcile.drifted.length > 0 && (
                <Tag color="warning">
                  {t("pages.connectors.classes.drifted")}:{" "}
                  {reconcile.drifted.length}
                </Tag>
              )}
              {reconcile.extra.length > 0 && (
                <Tag>
                  {t("pages.connectors.classes.extra")}:{" "}
                  {reconcile.extra.length}
                </Tag>
              )}
              {view?.strict_mode && (
                <Tag color="purple">{t("pages.connectors.strict")}</Tag>
              )}
            </Space>
          }
        />
      )}
      <FilterSearchBar
        filters={[
          {
            key: "connector-type",
            placeholder: t("pages.connectors.filters.allTypes"),
            value: typeFilter,
            onChange: setTypeFilter,
            options: typeFilterOptions,
          },
          {
            key: "connector-state",
            placeholder: t("pages.connectors.filters.allStates"),
            value: stateFilter,
            onChange: setStateFilter,
            options: stateFilterOptions,
          },
          {
            key: "connector-reconcile",
            placeholder: t("pages.connectors.filters.allReconcile"),
            value: reconcileFilter,
            onChange: setReconcileFilter,
            options: reconcileFilterOptions,
          },
          {
            key: "connector-binding",
            placeholder: t("pages.connectors.filters.allBindings"),
            value: bindingFilter,
            onChange: setBindingFilter,
            options: [
              {
                value: "gateway",
                label: t("pages.connectors.filters.bindingGateway"),
              },
              {
                value: "device",
                label: t("pages.connectors.filters.bindingDevice"),
              },
            ],
          },
        ]}
        searchValue={keyword}
        searchPlaceholder={t("pages.connectors.filters.searchPlaceholder")}
        onSearchChange={setKeyword}
      />
      <Table
        rowKey="spec_id"
        loading={specsQuery.isLoading}
        columns={columns}
        dataSource={filteredSpecs}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("pages.connectors.empty") }}
      />
      <Drawer
        open={modalOpen}
        title={
          editing
            ? t("pages.connectors.editTitle")
            : t("pages.connectors.addTitle")
        }
        placement="right"
        width={760}
        onClose={() => setModalOpen(false)}
        destroyOnHidden
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button onClick={() => setModalOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              loading={saveMutation.isPending}
              onClick={() => form.submit()}
            >
              {t("common.save")}
            </Button>
          </Space>
        }
        afterOpenChange={(visible) => {
          if (visible && editing) {
            form.setFieldsValue({
              connector_type: editing.connector_type,
              type_version: editing.type_version,
              scope: editing.device_id ? "devices" : "gateway",
              device_id: editing.device_id || undefined,
              device_ids: editing.device_id ? [editing.device_id] : [],
              profile_id: editing.profile_id || undefined,
              configText: JSON.stringify(editing.config ?? {}, null, 2),
              mappingsText: JSON.stringify(editing.mappings ?? [], null, 2),
              hc_enabled: !!editing.health_check?.resource,
              hc_resource: editing.health_check?.resource,
              hc_interval: editing.health_check?.interval_ms ?? 30000,
              hc_timeout: editing.health_check?.timeout_ms ?? 5000,
              hc_threshold: editing.health_check?.fail_threshold ?? 3,
            });
          }
        }}
      >
        <Form
          form={form}
          layout="vertical"
          preserve={false}
          initialValues={{
            type_version: "",
            scope: "gateway",
            device_id: undefined,
            device_ids: [],
            profile_id: undefined,
            configText: "{}",
            mappingsText: "[]",
          }}
          onFinish={(values) => saveMutation.mutate(values)}
        >
          <Divider orientation="left" plain style={{ marginTop: 0 }}>
            {t("pages.connectors.sections.connector")}
          </Divider>
          <Form.Item
            label={t("pages.connectors.form.type")}
            name="connector_type"
            rules={[
              {
                required: true,
                message: t("pages.connectors.errors.typeRequired"),
              },
            ]}
          >
            <AutoComplete
              options={typeOptions}
              placeholder="nereid-edge"
              filterOption
            />
          </Form.Item>
          <Form.Item
            label={t("pages.connectors.form.version")}
            name="type_version"
          >
            <AutoComplete
              options={versionOptions}
              placeholder={t("pages.connectors.form.versionHint")}
              filterOption
            />
          </Form.Item>
          <Form.Item
            label={t("pages.connectors.form.profile")}
            name="profile_id"
          >
            <Select
              allowClear
              placeholder={t("pages.connectors.form.profileHint")}
              notFoundContent={t("pages.connectors.form.noTemplateProfiles")}
              options={profiles
                .filter((profile: Profile) => !!profile.connector_type)
                .map((profile: Profile) => ({
                  value: profile.id,
                  label: `${profile.name} (${profile.id.slice(0, 8)})`,
                }))}
            />
          </Form.Item>
          <Divider orientation="left" plain>
            {t("pages.connectors.sections.target")}
          </Divider>
          <Form.Item label={t("pages.connectors.form.target")} name="scope">
            <Segmented
              block
              options={[
                { value: "gateway", label: t("pages.connectors.scopeGateway") },
                { value: "devices", label: t("pages.connectors.scopeDevices") },
              ]}
            />
          </Form.Item>
          {/*
            The device pickers are always mounted and only hidden with CSS;
            conditionally unmounting them raced the afterOpenChange
            setFieldsValue (the field was not registered yet, so the bound
            sub-device came back empty when editing). A mounted field reads
            its value from the form store regardless of when the value was
            written. Required validation is switched on only while visible.
          */}
          <div
            style={{
              display:
                editing && targetScope === "devices" ? undefined : "none",
            }}
          >
            <Form.Item
              label={t("pages.connectors.form.device")}
              name="device_id"
              rules={
                editing && targetScope === "devices"
                  ? [
                      {
                        validator: (_: unknown, value?: string) =>
                          value
                            ? Promise.resolve()
                            : Promise.reject(
                                new Error(
                                  t("pages.connectors.errors.devicesRequired"),
                                ),
                              ),
                      },
                    ]
                  : []
              }
            >
              <Select
                showSearch
                optionFilterProp="label"
                placeholder={t("pages.connectors.form.device")}
                options={editDeviceOptions}
              />
            </Form.Item>
          </div>
          <div
            style={{
              display:
                !editing && targetScope === "devices" ? undefined : "none",
            }}
          >
            <Form.Item
              label={t("pages.connectors.form.devices")}
              required
              extra={
                managed.length === 0
                  ? t("pages.connectors.noSubDevices")
                  : t("pages.connectors.form.devicesHint")
              }
            >
              <Form.Item
                name="device_ids"
                noStyle
                rules={
                  !editing && targetScope === "devices"
                    ? [
                        {
                          validator: (_: unknown, value?: string[]) =>
                            (value ?? []).length > 0
                              ? Promise.resolve()
                              : Promise.reject(
                                  new Error(
                                    t(
                                      "pages.connectors.errors.devicesRequired",
                                    ),
                                  ),
                                ),
                        },
                      ]
                    : []
                }
              >
                <Select
                  mode="multiple"
                  allowClear
                  showSearch
                  optionFilterProp="label"
                  style={{ width: "100%" }}
                  placeholder={t("pages.connectors.form.devicesPlaceholder")}
                  options={deviceOptions}
                  notFoundContent={
                    selectableManaged.length === 0 && managed.length > 0
                      ? t("pages.connectors.allBound")
                      : t("pages.connectors.noSubDevices")
                  }
                  popupRender={(menu) => (
                    <>
                      <Space style={{ padding: "4px 8px" }}>
                        <Button
                          type="link"
                          size="small"
                          disabled={selectableManaged.length === 0}
                          onClick={() =>
                            form.setFieldValue(
                              "device_ids",
                              selectableManaged.map(
                                (device: Device) => device.id,
                              ),
                            )
                          }
                        >
                          {t("pages.connectors.selectAll")}
                        </Button>
                        <Button
                          type="link"
                          size="small"
                          onClick={() => form.setFieldValue("device_ids", [])}
                        >
                          {t("pages.connectors.clearAll")}
                        </Button>
                      </Space>
                      {menu}
                    </>
                  )}
                />
              </Form.Item>
            </Form.Item>
          </div>
          <Divider orientation="left" plain>
            {t("pages.connectors.sections.config")}
          </Divider>
          <Form.Item
            label={t("pages.connectors.form.config")}
            name="configText"
          >
            <JsonEditor
              kind="config"
              connectorType={selectedType}
              schema={configSchema}
              partial={!!selectedProfile}
              deviceNameDefault={deviceNameDefault}
              deviceNameFallbackList={multiDeviceNames}
              gatewayId={gatewayId}
              protocolFamily={selectedTemplate?.protocol}
              templateDoc={selectedProfile ? templateConfig : null}
              templateMappings={selectedProfile ? templateMappingsList : null}
              pointNameOptions={templatePointNames}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.connectors.form.mappings")}
            name="mappingsText"
          >
            <JsonEditor
              kind="mappings"
              templateMappings={selectedProfile ? templateMappingsList : null}
              pointNameOptions={templatePointNames}
            />
          </Form.Item>
          {/*
            Same always-mounted/CSS-hidden pattern as the device pickers above:
            these fields are written by the afterOpenChange setFieldsValue in
            edit mode, and conditionally mounting them raced that write, so a
            saved health_check reopened as disabled/empty despite the value
            living in the form store.
          */}
          <div
            style={{ display: targetScope === "devices" ? undefined : "none" }}
          >
            <Divider orientation="left" plain>
              {t("pages.connectors.sections.healthCheck")}
            </Divider>
            <Form.Item
              name="hc_enabled"
              valuePropName="checked"
              label={t("pages.connectors.form.healthCheckEnabled")}
              extra={t("pages.connectors.form.healthCheckHint")}
            >
              <Switch />
            </Form.Item>
            <Form.Item
              noStyle
              shouldUpdate={(prev, cur) =>
                prev.hc_enabled !== cur.hc_enabled || prev.scope !== cur.scope
              }
            >
              {({ getFieldValue }) => (
                <div
                  style={{
                    display:
                      getFieldValue("hc_enabled") && targetScope === "devices"
                        ? undefined
                        : "none",
                  }}
                >
                  <Form.Item
                    name="hc_resource"
                    label={t("pages.connectors.form.healthCheckResource")}
                    rules={[
                      {
                        validator: (_: unknown, v?: string) =>
                          !getFieldValue("hc_enabled") || (v && v.trim())
                            ? Promise.resolve()
                            : Promise.reject(
                                new Error(
                                  t(
                                    "pages.connectors.errors.healthCheckResource",
                                  ),
                                ),
                              ),
                      },
                    ]}
                  >
                    <AutoComplete
                      showSearch
                      options={healthResourceOptions}
                      filterOption={(input, option) =>
                        String(option?.value ?? "")
                          .toLowerCase()
                          .includes(input.toLowerCase())
                      }
                      placeholder={t(
                        "pages.connectors.form.healthCheckResourceHint",
                      )}
                    />
                  </Form.Item>
                  <Space size={8} align="start" style={{ display: "flex" }}>
                    <Form.Item
                      name="hc_interval"
                      label={t("pages.connectors.form.healthCheckInterval")}
                      style={{ flex: 1 }}
                      initialValue={30000}
                    >
                      <InputNumber
                        min={1}
                        max={86400000}
                        addonAfter="ms"
                        style={{ width: "100%" }}
                      />
                    </Form.Item>
                    <Form.Item
                      name="hc_timeout"
                      label={t("pages.connectors.form.healthCheckTimeout")}
                      style={{ flex: 1 }}
                      initialValue={5000}
                    >
                      <InputNumber
                        min={1}
                        max={60000}
                        addonAfter="ms"
                        style={{ width: "100%" }}
                      />
                    </Form.Item>
                    <Form.Item
                      name="hc_threshold"
                      label={t("pages.connectors.form.healthCheckThreshold")}
                      style={{ flex: 1 }}
                      initialValue={3}
                    >
                      <InputNumber
                        min={1}
                        max={100}
                        style={{ width: "100%" }}
                      />
                    </Form.Item>
                  </Space>
                </div>
              )}
            </Form.Item>
          </div>
        </Form>
      </Drawer>
    </Card>
  );
}
