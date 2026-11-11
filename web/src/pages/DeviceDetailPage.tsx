// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// DeviceDetailPage renders one device as an independent route: header with
// live presence badge, then Tabs (key synced to ?tab) for telemetry,
// attributes, commands and alarms.

import { Link } from "react-router-dom";
import { Alert, Button, Descriptions, Drawer, Input, Select, Space, Table, Tabs, Tag, Tooltip, Typography, message } from "antd";
import { QuestionCircleOutlined, EditOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router-dom";
import { useParams } from "react-router-dom";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import dayjs from "dayjs";

import PageHeader from "../components/PageHeader";
import DeviceFormDrawer from "../components/DeviceFormDrawer";
import FilterSearchBar from "../components/FilterSearchBar";
import StatusBadge from "../components/StatusBadge";
import AttributesTab from "../components/device/AttributesTab";
import CommandsTab from "../components/device/CommandsTab";
import ConnectorsTab from "../components/device/ConnectorsTab";
import CredentialsTab from "../components/device/CredentialsTab";
import DeviceDeliveryTab from "../components/device/DeviceDeliveryTab";
import DeviceAlarmsTab from "../components/device/DeviceAlarmsTab";
import DeviceOtaTab from "../components/device/DeviceOtaTab";
import EdgeProfilesTab from "../components/device/EdgeProfilesTab";
import LatestTelemetryStrip from "../components/device/LatestTelemetryStrip";
import LinkHealthTab from "../components/device/LinkHealthTab";
import LiveTelemetryTab from "../components/device/LiveTelemetryTab";
import ModelInventoryTab from "../components/device/ModelInventoryTab";
import SecretsTab from "../components/device/SecretsTab";
import OperationsTab from "../components/device/OperationsTab";
import ProfileDeliveryTab from "../components/device/ProfileDeliveryTab";
import ShadowTab from "../components/device/ShadowTab";
import { useOnlineMap } from "../hooks/useOnlineMap";
import { usePresenceSince } from "../hooks/usePresenceSince";
import { getDevice, listDevicesByGateway, type Device } from "../api/devices";
import { listProfiles } from "../api/profiles";
import { listGroups } from "../api/ruleChains";
import { quarantineDevice, releaseFromQuarantine, fetchQuarantineReasonCodes, listCustomers, listDeviceGroups } from "../api/ownership";
import { authStore } from "../store/authStore";
import { useHelpStore } from "../help/helpStore";
import { ConnectorStateColor } from "../utils/connectorState";

// formatTime renders an RFC3339 timestamp in the locale's date-time format.
function formatTime(value: string, language: string): string {
  return dayjs(value).format(
    language.startsWith("zh") ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A",
  );
}

// tabHelpDocs maps a tab to the help document that explains it, so the
// tab bar can offer help scoped to whatever tab is currently open.
const tabHelpDocs: Record<string, string> = {
  telemetry: "data-quality",
  attributes: "device-detail",
  shadow: "device-shadow",
  credentials: "credentials",
  commands: "commands",
  alarms: "alarms",
  connectors: "connectors",
  models: "edge-models",
  secrets: "edge-secrets",
  delivery: "device-detail",
  ota: "ota-tasks",
  operations: "gateway-operations",
  edgeProfiles: "edge-profiles",
  profileDelivery: "profile-delivery",
  linkHealth: "link-health",
};

// tabKeys are the allowed URL tab values, in display order.
const tabKeys = ["telemetry", "attributes", "shadow", "credentials", "commands", "alarms"] as const;

// DeviceDetailPage renders the detail route.
export default function DeviceDetailPage() {
  const { t, i18n } = useTranslation();
  const { deviceId = "" } = useParams();
  const [searchParams, setSearchParams] = useSearchParams();

  const deviceQuery = useQuery({
    queryKey: ["device", deviceId],
    queryFn: () => getDevice(deviceId),
    enabled: !!deviceId,
  });

  const onlineMap = useOnlineMap();
  const liveBoundary = usePresenceSince().get(deviceId);
  const state = liveBoundary?.state ?? onlineMap.get(deviceId) ?? "UNKNOWN";
  const since = liveBoundary?.time;

  // Profiles are a small set; resolve the bound profile ID to its name for
  // display and deep-link into the profile modal.
  const profilesQuery = useQuery({
    queryKey: ["profiles"],
    queryFn: listProfiles,
    staleTime: 0,
  });
  const device = deviceQuery.data;

  const isPlatformAdmin = authStore((state) => state.user?.is_platform_admin ?? false);
  const [quarantineOpen, setQuarantineOpen] = useState(false);
  const [quarantineCode, setQuarantineCode] = useState("");
  const [quarantineNote, setQuarantineNote] = useState("");
  const [quarantineBusy, setQuarantineBusy] = useState(false);

  // The accepted reason vocabulary is owned by the backend; fetching it
  // keeps the console in sync when codes are added.
  const quarantineReasonsQuery = useQuery({
    queryKey: ["quarantine-reasons"],
    queryFn: fetchQuarantineReasonCodes,
    staleTime: 5 * 60_000,
  });
  // The backend marks a few codes (currently "other") note-mandatory.
  const quarantineNoteRequired =
    quarantineReasonsQuery.data?.note_required.includes(quarantineCode) ?? false;
  const quarantineValid =
    quarantineCode !== "" && (!quarantineNoteRequired || quarantineNote.trim() !== "");

  const submitQuarantine = async () => {
    if (!quarantineValid) {
      return;
    }
    setQuarantineBusy(true);
    try {
      await quarantineDevice(deviceId, quarantineCode, quarantineNote.trim());
      message.success(t("pages.deviceDetail.quarantine.success"));
      setQuarantineOpen(false);
      setQuarantineCode("");
      setQuarantineNote("");
      deviceQuery.refetch();
    } finally {
      setQuarantineBusy(false);
    }
  };

  const [releaseOpen, setReleaseOpen] = useState(false);
  const [releaseNote, setReleaseNote] = useState("");
  const [releaseBusy, setReleaseBusy] = useState(false);

  const submitRelease = async () => {
    if (!releaseNote.trim()) {
      return;
    }
    setReleaseBusy(true);
    try {
      await releaseFromQuarantine(deviceId, releaseNote.trim());
      message.success(t("pages.deviceDetail.quarantine.release.success"));
      setReleaseOpen(false);
      setReleaseNote("");
      deviceQuery.refetch();
    } finally {
      setReleaseBusy(false);
    }
  };

  const boundProfile = profilesQuery.data?.find(
    (profile) => profile.id === device?.profile_id,
  );

  // Ownership/chain lists are small and shared across pages; resolve the
  // raw IDs on the device row to names and links.
  const customersQuery = useQuery({
    queryKey: ["customers"],
    queryFn: listCustomers,
  });
  const deviceGroupsQuery = useQuery({
    queryKey: ["device-groups"],
    queryFn: listDeviceGroups,
  });
  const chainGroupsQuery = useQuery({
    queryKey: ["rule-chain-groups"],
    queryFn: listGroups,
  });

  const boundCustomer = customersQuery.data?.find(
    (customer) => customer.id === device?.customer_id,
  );
  const boundDeviceGroup = deviceGroupsQuery.data?.find(
    (group) => group.id === device?.group_id,
  );

  // Effective rule chain follows the ADR-0028 priority entirely on the
  // client: device override, then profile binding, then tenant default.
  // The source tag tells operators why this chain applies.
  const effectiveChain = (() => {
    const groups = chainGroupsQuery.data ?? [];
    if (device?.rule_chain_group_id) {
      return { group: groups.find((g) => g.id === device.rule_chain_group_id), source: "device" as const };
    }
    if (boundProfile?.rule_chain_group_id) {
      return { group: groups.find((g) => g.id === boundProfile.rule_chain_group_id), source: "profile" as const };
    }
    return { group: groups.find((g) => g.is_default), source: "default" as const };
  })();

  const [editOpen, setEditOpen] = useState(false);

  // Fillers keep the 3-column grid rows whole: 10 fixed items plus the
  // optional gateway and connector rows; pad the remainder up to 3.
  const descriptionItemCount =
    10 +
    (device?.gateway_id ? 1 : 0) +
    (device?.is_gateway || device?.connector_type ? 1 : 0);
  const fillerCount = (3 - (descriptionItemCount % 3)) % 3;

  // Secrets feature support comes from the last hello negotiation stamped
  // on the gateway row (ADR-0034); absent means unsupported firmware.
  const supportsSecrets = (device?.agent_features ?? []).includes("secrets");
  // Profile downlink support comes from the profiles feature bit stamped on
  // the gateway during hello negotiation (ADR-0038); absent = older firmware.
  const supportsProfileDelivery = (device?.agent_features ?? []).includes(
    "profiles",
  );
  // Edge model inference support comes from the inference feature bit
  // stamped during hello negotiation (ADR-0039); absent = older firmware.
  const supportsInference = (device?.agent_features ?? []).includes(
    "inference",
  );

  const requested = searchParams.get("tab") ?? tabKeys[0];
  const validTabs = useMemo<string[]>(() => {
    const keys: string[] = [...tabKeys];
    if (device?.is_gateway) {
      keys.push("managed");
      keys.push("linkHealth");
      keys.push("connectors");
      keys.push("models");
      keys.push("secrets");
      keys.push("operations");
      keys.push("edgeProfiles");
      keys.push("profileDelivery");
    }
    // Sub-devices show a read-only connector + secret delivery summary;
    // gateways manage those surfaces on their own tabs (managed/connectors/
    // secrets are already added above).
    if (!device?.is_gateway && !!device?.gateway_id) {
      keys.push("delivery");
    }
    // Direct devices (no owning gateway) get their own firmware OTA tab.
    if (!device?.is_gateway && !device?.gateway_id) {
      keys.push("ota");
    }
    return keys;
  }, [device?.is_gateway, device?.gateway_id]);
  const activeKey = validTabs.includes(requested) ? requested : tabKeys[0];
  const openHelp = useHelpStore((state) => state.open);

  if (deviceQuery.isError) {
    return (
      <>
        <PageHeader title={t("pages.deviceDetail.fallback")} helpId="device-detail" />
        <Alert type="error" showIcon message={t("pages.deviceDetail.notFound")} />
      </>
    );
  }

  const items = [
    {
      key: "telemetry",
      label: t("pages.deviceDetail.tabs.telemetry"),
      children: <LiveTelemetryTab deviceId={deviceId} />,
    },
    {
      key: "attributes",
      label: t("pages.deviceDetail.tabs.attributes"),
      children: <AttributesTab deviceId={deviceId} />,
    },
    {
      key: "shadow",
      label: t("pages.deviceDetail.tabs.shadow"),
      children: <ShadowTab deviceId={deviceId} />,
    },
    {
      key: "credentials",
      label: t("pages.deviceDetail.tabs.credentials"),
      children: (
        <CredentialsTab
          deviceId={deviceId}
          canonicalName={device?.canonical_name}
        />
      ),
    },
    {
      key: "commands",
      label: t("pages.deviceDetail.tabs.commands"),
      children: <CommandsTab deviceId={deviceId} />,
    },
    {
      key: "alarms",
      label: t("pages.deviceDetail.tabs.alarms"),
      children: <DeviceAlarmsTab deviceId={deviceId} />,
    },
    ...(!device?.is_gateway && device?.gateway_id
      ? [
          {
            key: "delivery",
            label: t("pages.deviceDetail.tabs.delivery"),
            children: (
              <DeviceDeliveryTab
                deviceId={deviceId}
                gatewayId={device.gateway_id}
              />
            ),
          },
        ]
      : []),
    ...(!device?.is_gateway && !device?.gateway_id
      ? [
          {
            key: "ota",
            label: t("pages.deviceDetail.tabs.ota"),
            children: <DeviceOtaTab deviceId={deviceId} />,
          },
        ]
      : []),
    ...(device?.is_gateway
      ? [
          {
            key: "managed",
            label: t("pages.deviceDetail.tabs.managed"),
            children: <ManagedDevicesTab deviceId={deviceId} />,
          },
          {
            key: "linkHealth",
            label: t("pages.deviceDetail.tabs.linkHealth"),
            children: <LinkHealthTab gatewayId={deviceId} />,
          },
          {
            key: "connectors",
            label: t("pages.deviceDetail.tabs.connectors"),
            children: <ConnectorsTab gatewayId={deviceId} />,
          },
          {
            key: "models",
            label: t("pages.deviceDetail.tabs.models"),
            children: (
              <ModelInventoryTab
                gatewayId={deviceId}
                supported={supportsInference}
              />
            ),
          },
          {
            key: "secrets",
            label: t("pages.deviceDetail.tabs.secrets"),
            children: (
              <SecretsTab
                gatewayId={deviceId}
                supported={supportsSecrets}
              />
            ),
          },
          {
            key: "edgeProfiles",
            label: t("pages.deviceDetail.tabs.edgeProfiles"),
            children: <EdgeProfilesTab gatewayId={deviceId} />,
          },
          {
            key: "profileDelivery",
            label: t("pages.deviceDetail.tabs.profileDelivery"),
            children: (
              <ProfileDeliveryTab
                gatewayId={deviceId}
                supported={supportsProfileDelivery}
              />
            ),
          },
          {
            key: "operations",
            label: t("pages.deviceDetail.tabs.operations"),
            children: <OperationsTab device={device} />,
          },
        ]
      : []),
  ];

  return (
    <>
      <PageHeader
        title={device?.canonical_name ?? t("pages.deviceDetail.fallback")}
        subtitle={device?.alias
          ? t("pages.deviceDetail.aliasPattern", { alias: device.alias })
          : t("pages.deviceDetail.idPattern", { id: deviceId })}
        extra={
          <>
            <Button icon={<EditOutlined />} onClick={() => setEditOpen(true)}>
              {t("pages.deviceDetail.editButton")}
            </Button>
            {isPlatformAdmin && device?.state === "ACTIVE" && (
              <Button danger onClick={() => setQuarantineOpen(true)}>
                {t("pages.deviceDetail.quarantine.button")}
              </Button>
            )}
            {isPlatformAdmin && device?.state === "QUARANTINED" && (
              <Button type="primary" onClick={() => setReleaseOpen(true)}>
                {t("pages.deviceDetail.quarantine.release.button")}
              </Button>
            )}
            <BadgeWithSince state={state} since={since ?? device?.presence_since} language={i18n.language} />
          </>
        }
        helpId="device-detail"
      />
      <Descriptions
        column={3}
        bordered
        size="small"
        style={{ marginBottom: 16, background: "#fff", borderRadius: 8 }}
      >
        <Descriptions.Item label={t("pages.deviceDetail.labels.profileId")}>
          {boundProfile ? (
            <Link to={`/profiles/${boundProfile.id}`}>
              {boundProfile.name}
            </Link>
          ) : (
            "-"
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.customer")}>
          {boundCustomer ? (
            <Link to="/customers">{boundCustomer.name}</Link>
          ) : (
            "-"
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.group")}>
          {boundDeviceGroup ? (
            <Link to={`/devices?group_id=${boundDeviceGroup.id}`}>
              {boundDeviceGroup.name}
            </Link>
          ) : (
            "-"
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.ruleChain")}>
          {effectiveChain.group ? (
            <Space size={4}>
              <Link to={`/rule-chains/g/${effectiveChain.group.id}`}>
                {effectiveChain.group.name}
              </Link>
              <Tag color={effectiveChain.source === "default" ? "default" : "blue"}>
                {effectiveChain.source === "device"
                  ? t("pages.deviceDetail.chainSource.device")
                  : effectiveChain.source === "profile"
                    ? t("pages.deviceDetail.chainSource.profile")
                    : t("pages.ruleChains.defaultTag")}
              </Tag>
              {effectiveChain.group.active_chain_id ? (
                <Tag>v{effectiveChain.group.active_version}</Tag>
              ) : (
                <Tag color="warning">{t("pages.deviceDetail.noActiveChain")}</Tag>
              )}
            </Space>
          ) : (
            "-"
          )}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.deviceId")}>
          {deviceId}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.lifecycle")}>
          {device ? <StatusBadge status={device.state} /> : "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.type")}>
          {device?.is_gateway
            ? t("pages.deviceDetail.types.gateway")
            : device?.gateway_id
              ? t("pages.deviceDetail.types.subdevice")
              : "-"}
        </Descriptions.Item>
        {device?.gateway_id && (
          <Descriptions.Item label={t("pages.deviceDetail.labels.gateway")}>
            <Link to={`/devices/${device.gateway_id}`}>
              {device.gateway_name || device.gateway_id}
            </Link>
          </Descriptions.Item>
        )}
        {(device?.is_gateway || device?.connector_type) && (
          // Gateways show their gateway-level instance count; sub-devices
          // show their one bound connector with apply state, deep-linking to
          // the owning gateway's Connectors tab.
          <Descriptions.Item label={t("pages.deviceDetail.labels.connector")}>
            {device?.is_gateway ? (
              <Link to={`/devices/${deviceId}?tab=connectors`}>
                <Tag color="geekblue">
                  {t("pages.deviceDetail.connectorGatewayLevel", {
                    count: device.gateway_connector_count ?? 0,
                  })}
                </Tag>
              </Link>
            ) : device?.connector_type ? (
              <Link to={`/devices/${device.gateway_id}?tab=connectors`}>
                <Tag color={ConnectorStateColor[device.connector_state ?? ""] ?? "default"}>
                  {device.connector_type}
                </Tag>
              </Link>
            ) : (
              "-"
            )}
          </Descriptions.Item>
        )}
        <Descriptions.Item label={t("pages.deviceDetail.labels.firmware")}>
          {device?.firmware_version || "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.lastActivity")}>
          {device?.last_activity_at ? formatTime(device.last_activity_at, i18n.language) : "-"}
        </Descriptions.Item>
        <Descriptions.Item label={t("pages.deviceDetail.labels.createdAt")}>
          {device?.created_at ? formatTime(device.created_at, i18n.language) : "-"}
        </Descriptions.Item>
        {Array.from({ length: fillerCount }, (_, index) => (
          <Descriptions.Item key={`filler-${index}`} label=" ">{"\u00a0"}</Descriptions.Item>
        ))}
        <Descriptions.Item
          label={t("pages.deviceDetail.labels.notes")}
          span={3}
        >
          {device?.notes
            ? device.notes.split("\n").map((line) => <div key={line}>{line}</div>)
            : "-"}
        </Descriptions.Item>
      </Descriptions>
      <LatestTelemetryStrip deviceId={deviceId} />
      <Tabs
        activeKey={activeKey}
        onChange={(key) => setSearchParams({ tab: key })}
        items={items}
        tabBarExtraContent={
          <Tooltip title={t("nav.help")}>
            <Button
              type="text"
              icon={<QuestionCircleOutlined />}
              onClick={() => openHelp(tabHelpDocs[activeKey] ?? "device-detail")}
            />
          </Tooltip>
        }
        destroyOnHidden
      />
      <Drawer
        open={quarantineOpen}
        width={720}
        onClose={() => setQuarantineOpen(false)}
        footer={
          <Space style={{ marginLeft: "auto" }}>
            <Button onClick={() => setQuarantineOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              danger
              disabled={!quarantineValid}
              loading={quarantineBusy}
              onClick={submitQuarantine}
            >
              {t("pages.deviceDetail.quarantine.confirm")}
            </Button>
          </Space>
        }
        title={
          <Space>
            {t("pages.deviceDetail.quarantine.title")}
            <Tooltip title={t("nav.help")}>
              <Button
                type="text"
                size="small"
                icon={<QuestionCircleOutlined />}
                onClick={() => openHelp("quarantine")}
              />
            </Tooltip>
          </Space>
        }
      >
        <div style={{ marginBottom: 8 }}>
          {t("pages.deviceDetail.quarantine.reasonLabel")}
        </div>
        <Select
          style={{ width: "100%" }}
          value={quarantineCode || undefined}
          onChange={setQuarantineCode}
          placeholder={t("pages.deviceDetail.quarantine.reasonPlaceholder")}
          options={(quarantineReasonsQuery.data?.reason_codes ?? []).map((code) => ({
            value: code,
            label: code,
          }))}
        />
        <div style={{ margin: "16px 0 8px" }}>
          {t("pages.deviceDetail.quarantine.noteLabel")}
          {quarantineNoteRequired ? ` (${t("pages.deviceDetail.quarantine.requiredSuffix")})` : ""}
        </div>
        <Input.TextArea
          rows={3}
          value={quarantineNote}
          onChange={(event) => setQuarantineNote(event.target.value)}
          placeholder={t("pages.deviceDetail.quarantine.notePlaceholder")}
        />
      </Drawer>
      <Drawer
        open={releaseOpen}
        width={720}
        title={t("pages.deviceDetail.quarantine.release.title")}
        onClose={() => setReleaseOpen(false)}
        footer={
          <Space style={{ marginLeft: "auto" }}>
            <Button onClick={() => setReleaseOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              disabled={!releaseNote.trim()}
              loading={releaseBusy}
              onClick={submitRelease}
            >
              {t("pages.deviceDetail.quarantine.release.confirm")}
            </Button>
          </Space>
        }
      >
        <div style={{ marginBottom: 8 }}>
          {t("pages.deviceDetail.quarantine.release.noteLabel")}
        </div>
        <Input.TextArea
          rows={3}
          value={releaseNote}
          onChange={(event) => setReleaseNote(event.target.value)}
          placeholder={t("pages.deviceDetail.quarantine.release.notePlaceholder")}
        />
      </Drawer>
      <DeviceFormDrawer
        open={editOpen}
        device={device}
        profiles={profilesQuery.data ?? []}
        onClose={() => setEditOpen(false)}
      />
    </>
  );
}

// BadgeWithSince renders the presence badge whose tooltip shows the
// localized absolute time of the current state boundary. Without a
// known boundary the tooltip falls back to the state label.
function BadgeWithSince({
  state,
  since,
  language,
}: {
  state: string;
  since?: string;
  language: string;
}) {
  const { t } = useTranslation();
  const isZh = language.startsWith("zh");
  const tooltip = since
    ? dayjs(since).format(isZh ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A")
    : t(`status.${state.toLowerCase()}`, { defaultValue: state });

  return (
    <Tooltip title={tooltip}>
      <StatusBadge status={state} />
    </Tooltip>
  );
}

// ManagedDevicesTab lists every sub-device bound to a gateway device, so an
// operator can see at a glance what a gateway manages (ADR-0010).
function ManagedDevicesTab({ deviceId }: { deviceId: string }) {
  const { t, i18n } = useTranslation();
  const managedQuery = useQuery({
    queryKey: ["managed-devices", deviceId],
    queryFn: () => listDevicesByGateway(deviceId),
  });
  // Memoized so derived memos keep a stable dependency identity instead
  // of seeing a fresh [] fallback on every render.
  const managed = useMemo(() => managedQuery.data ?? [], [managedQuery.data]);

  // Resolve profile ids to names; the profile set is small and shared.
  const profilesQuery = useQuery({
    queryKey: ["profiles"],
    queryFn: listProfiles,
  });
  const profileName = (id?: string) =>
    id
      ? (profilesQuery.data?.find((p) => p.id === id)?.name ?? id)
      : "";

  const [keyword, setKeyword] = useState("");

  // Filtering is client-side; a gateway manages a bounded set of devices.
  // Match canonical name, alias or id (case-insensitive partial match).
  const filteredManaged = useMemo(() => {
    const term = keyword.trim().toLowerCase();
    if (!term) return managed;
    return managed.filter(
      (device) =>
        device.canonical_name.toLowerCase().includes(term) ||
        (device.alias ?? "").toLowerCase().includes(term) ||
        device.id.toLowerCase().includes(term),
    );
  }, [managed, keyword]);

  const columns = [
    {
      title: t("common.name"),
      dataIndex: "canonical_name",
      render: (name: string, device: Device) => (
        <Space direction="vertical" size={0}>
          <Link to={`/devices/${device.id}`}>{name}</Link>
          {device.alias && (
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>
              {device.alias}
            </Typography.Text>
          )}
        </Space>
      ),
    },
    {
      title: t("pages.devices.profileColumn"),
      dataIndex: "profile_id",
      render: (id?: string) => {
        const name = profileName(id);
        if (!name) {
          return <Typography.Text type="secondary">-</Typography.Text>;
        }
        return <Link to={`/profiles/${id}`}>{name}</Link>;
      },
    },
    {
      title: t("pages.devices.connectorColumn"),
      key: "connector",
      render: (_: unknown, device: Device) => {
        if (!device.connector_type) {
          return <Typography.Text type="secondary">-</Typography.Text>;
        }
        // Jump to this gateway's Connectors tab, which owns the spec and
        // its reported state.
        return (
          <Link to={`/devices/${deviceId}?tab=connectors`}>
            <Tag>{device.connector_type}</Tag>
          </Link>
        );
      },
    },
    {
      title: t("pages.devices.connectionColumn"),
      key: "presence",
      width: 130,
      render: (_: unknown, device: Device) => {
        const badge = (
          <StatusBadge status={device.presence_state ?? "UNKNOWN"} />
        );
        if (!device.last_activity_at) return badge;
        const fmt = i18n.language.startsWith("zh")
          ? "YYYY/MM/DD HH:mm:ss"
          : "M/D/YYYY, h:mm:ss A";
        return (
          <Tooltip
            title={`${t("pages.devices.lastActivity")} ${dayjs(
              device.last_activity_at,
            ).format(fmt)}`}
          >
            {badge}
          </Tooltip>
        );
      },
    },
    {
      title: t("pages.deviceDetail.labels.lifecycle"),
      dataIndex: "state",
      render: (state: string) => <Tag>{t(`pages.devices.states.${state}`)}</Tag>,
    },
  ];

  return (
    <>
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t(
          "pages.deviceDetail.managedSearchPlaceholder",
        )}
      />
      <Table
        rowKey="id"
        loading={managedQuery.isLoading}
        columns={columns}
        dataSource={filteredManaged}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("pages.deviceDetail.managedEmpty") }}
      />
    </>
  );
}
