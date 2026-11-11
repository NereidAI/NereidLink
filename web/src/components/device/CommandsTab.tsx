// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

/*
 * CommandsTab renders the command whitelist (catalog), the command dispatch
 * drawer and command history for one device.
 *
 * Send flow: a command must exist in the catalog; for profile-bound devices
 * the picker lists catalog entries that correspond to profile points
 * (measurement key = south resource name) and the read/write direction is
 * constrained by the catalog mask. Devices without a profile keep a free
 * command-name input as the manual fallback.
 */

import { useCallback, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, Drawer, Form, Input, InputNumber, Modal, Radio,
  Select, Space, Spin, Switch, Table, Tag, Tooltip, App } from "antd";
import {
  CheckCircleOutlined, CloseCircleOutlined, DeleteOutlined, EditOutlined,
  ExclamationCircleOutlined, PlusOutlined, SendOutlined,
} from "@ant-design/icons";
import { useTranslation } from "react-i18next";
import {
  createCommandCatalogEntry, deleteCommandCatalogEntry, getDevice,
  listCommandCatalog, listCommandHistory, sendCommand, updateCommandCatalogEntry,
  type CommandCatalogEntry, type CommandCatalogInput, type CommandRecord,
} from "../../api/devices";
import { listProfiles, type Descriptor } from "../../api/profiles";
import FilterSearchBar from "../FilterSearchBar";
import dayjs from "dayjs";

interface Props {
  deviceId: string;
}

type Direction = "read" | "write";

const CATALOG_QUERY = (deviceId: string) => ["command-catalog", deviceId] as const;
const HISTORY_KEY = ["device-command-history"] as const;

function asDirectionMask(d: Descriptor): { read: boolean; write: boolean } {
  switch ((d.read_write ?? "").toUpperCase()) {
    case "R":
      return { read: true, write: false };
    case "W":
      return { read: false, write: true };
    case "RW":
      return { read: true, write: true };
    default:
      return { read: false, write: false };
  }
}

function catalogDefaults(name: string, mask: { read: boolean; write: boolean }): CommandCatalogInput {
  return {
    name,
    interaction: "request-response",
    timeliness: "realtime",
    default_timeout_ms: 10000,
    allow_read: mask.read,
    allow_write: mask.write,
    enabled: true,
  };
}

export default function CommandsTab({ deviceId }: Props) {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();

  const deviceQ = useQuery({ queryKey: ["device", deviceId], queryFn: () => getDevice(deviceId) });
  const profilesQ = useQuery({ queryKey: ["profiles"], queryFn: listProfiles });
  const catalogQ = useQuery({
    queryKey: CATALOG_QUERY(deviceId),
    queryFn: () => listCommandCatalog(deviceId),
  });

  const profile = useMemo(
    () => profilesQ.data?.find((p) => p.id === deviceQ.data?.profile_id) ?? null,
    [profilesQ.data, deviceQ.data?.profile_id],
  );
  const points = useMemo(() => profile?.descriptors ?? [], [profile]);
  const pointByKey = useMemo(() => {
    const m = new Map<string, Descriptor>();
    for (const p of points) m.set(p.measurement_key, p);
    return m;
  }, [points]);

  const [catalogSearch, setCatalogSearch] = useState("");
  const filteredCatalog = useMemo(() => {
    const q = catalogSearch.trim().toLowerCase();
    if (!q) return catalogQ.data ?? [];
    return (catalogQ.data ?? []).filter((e) =>
      [e.name, e.description ?? "", e.source, e.interaction, e.timeliness]
        .join(" ")
        .toLowerCase()
        .includes(q));
  }, [catalogQ.data, catalogSearch]);

  const [catalogOpen, setCatalogOpen] = useState(false);
  const [editing, setEditing] = useState<CommandCatalogEntry | null>(null);
  const [catalogForm] = Form.useForm<CommandCatalogInput>();

  const [sendOpen, setSendOpen] = useState(false);
  const [sendForm] = Form.useForm();
  const [recordDrawer, setRecordDrawer] = useState<CommandRecord | null>(null);

  const invalidateCatalog = useCallback(
    () => qc.invalidateQueries({ queryKey: CATALOG_QUERY(deviceId) }),
    [qc, deviceId],
  );

  const createMutation = useMutation({
    mutationFn: (input: CommandCatalogInput) => createCommandCatalogEntry(deviceId, input),
    onSuccess: async () => {
      message.success(t("pages.deviceDetail.commands.catalog.saveSuccess"));
      setCatalogOpen(false);
      await invalidateCatalog();
    },
    onError: (e: Error) => message.error(e.message),
  });
  const updateMutation = useMutation({
    mutationFn: (input: CommandCatalogInput) =>
      updateCommandCatalogEntry(deviceId, input.name, input),
    onSuccess: async () => {
      message.success(t("pages.deviceDetail.commands.catalog.saveSuccess"));
      setCatalogOpen(false);
      await invalidateCatalog();
    },
    onError: (e: Error) => message.error(e.message),
  });
  const deleteMutation = useMutation({
    mutationFn: (name: string) => deleteCommandCatalogEntry(deviceId, name),
    onSuccess: async () => {
      message.success(t("pages.deviceDetail.commands.catalog.deleteSuccess"));
      await invalidateCatalog();
    },
    onError: (e: Error) => message.error(e.message),
  });
  const toggleMutation = useMutation({
    mutationFn: (entry: CommandCatalogEntry) =>
      updateCommandCatalogEntry(deviceId, entry.name, {
        name: entry.name,
        interaction: entry.interaction,
        timeliness: entry.timeliness,
        default_timeout_ms: entry.default_timeout_ms,
        allow_read: entry.allow_read,
        allow_write: entry.allow_write,
        enabled: !entry.enabled,
        description: entry.description,
      }),
    onSuccess: () => invalidateCatalog(),
    onError: (e: Error) => {
      message.error(e.message);
      invalidateCatalog();
    },
  });

  const sendMutation = useMutation({
    mutationFn: async () => {
      const v = await sendForm.validateFields();
      const params = (v.paramsText ?? "").trim()
        ? JSON.parse(v.paramsText)
        : {};
      const res = await sendCommand(
        deviceId, v.name, params,
        v.direction as Direction | undefined,
        v.timeoutMs as number | undefined,
      );
      return { params, res };
    },
    onSuccess: ({ res }) => {
      message.success(t("pages.deviceDetail.commands.result", { status: res.status }));
      setSendOpen(false);
      qc.invalidateQueries({ queryKey: HISTORY_KEY });
    },
    onError: (e: unknown) => {
      if (e instanceof SyntaxError) {
        message.error(t("pages.deviceDetail.commands.paramsInvalid"));
        return;
      }
      if (e instanceof Error && e.message) message.error(e.message);
    },
  });

  // catalogPointOptions lists every profile point (measurement key is the
  // south resource name) for the declaration dropdown.
  const catalogPointOptions = useMemo(
    () =>
      points.map((p) => ({
        value: p.measurement_key,
        label: `${p.measurement_key}${p.read_write ? ` (${p.read_write})` : ""}`,
        point: p,
      })),
    [points],
  );


  const openEdit = useCallback((entry: CommandCatalogEntry) => {
    setEditing(entry);
    catalogForm.setFieldsValue({
      name: entry.name,
      interaction: entry.interaction,
      timeliness: entry.timeliness,
      default_timeout_ms: entry.default_timeout_ms,
      allow_read: entry.allow_read,
      allow_write: entry.allow_write,
      enabled: entry.enabled,
      description: entry.description,
    });
    setCatalogOpen(true);
  }, [catalogForm]);

  const openSendForPoint = useCallback((pointKey?: string) => {
    const entries = catalogQ.data ?? [];
    const first = pointKey
      ? entries.find((e) => e.enabled && e.name === pointKey)
      : entries.find((e) => e.enabled);
    sendForm.setFieldsValue({
      name: first?.name ?? pointKey ?? "",
      direction: first?.allow_read ? "read" : "write",
      paramsText: "{}",
      timeoutMs: undefined,
    });
    setSendOpen(true);
  }, [catalogQ.data, sendForm]);

  const onSubmitCatalog = useCallback(async () => {
    const v = await catalogForm.validateFields();
    const input: CommandCatalogInput = { ...v, description: v.description?.trim() || undefined };
    if (editing) updateMutation.mutate(input);
    else createMutation.mutate(input);
  }, [catalogForm, editing, createMutation, updateMutation]);

  // historyTable is the dispatch ledger with state filtering.
  const [stateFilter, setStateFilter] = useState<string | undefined>();
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(10);
  const historyQ = useQuery({
    queryKey: [...HISTORY_KEY, deviceId, page, pageSize, stateFilter],
    queryFn: () => listCommandHistory(deviceId, page, pageSize, stateFilter),
  });

  const catalogColumns = [
    {
      title: t("pages.deviceDetail.commands.catalog.name"),
      dataIndex: "name",
      key: "name",
      render: (name: string) => {
        const point = pointByKey.get(name);
        return (
          <Space size={4}>
            <span>{name}</span>
            {point && (
              <Tooltip title={t("pages.deviceDetail.commands.catalog.boundPoint", {
                type: point.data_type ?? "-", rw: point.read_write ?? "-",
              })}
              >
                <Tag>{point.measurement_key}</Tag>
              </Tooltip>
            )}
          </Space>
        );
      },
    },
    {
      title: t("pages.deviceDetail.commands.catalog.directions"),
      key: "directions",
      render: (_: unknown, e: CommandCatalogEntry) => (
        <Space size={4}>
          {e.allow_read && <Tag color="blue">{t("pages.deviceDetail.commands.directionRead")}</Tag>}
          {e.allow_write && <Tag color="orange">{t("pages.deviceDetail.commands.directionWrite")}</Tag>}
        </Space>
      ),
    },
    { title: t("pages.deviceDetail.commands.catalog.interaction"), dataIndex: "interaction", key: "interaction" },
    { title: t("pages.deviceDetail.commands.catalog.timeliness"), dataIndex: "timeliness", key: "timeliness" },
    {
      title: t("pages.deviceDetail.commands.catalog.timeout"),
      dataIndex: "default_timeout_ms",
      key: "default_timeout_ms",
      render: (ms: number) => (ms ? `${ms} ms` : "-"),
    },
    {
      title: t("pages.deviceDetail.commands.catalog.source"),
      dataIndex: "source",
      key: "source",
      render: (s: string) => (
        <Tag color={s === "auto" ? "default" : "geekblue"}>
          {t(`pages.deviceDetail.commands.catalog.source_${s}`)}
        </Tag>
      ),
    },
    {
      title: t("pages.deviceDetail.commands.catalog.enabled"),
      dataIndex: "enabled",
      key: "enabled",
      render: (enabled: boolean, entry: CommandCatalogEntry) => (
        <Switch
          size="small"
          checked={enabled}
          loading={toggleMutation.isPending}
          onChange={() => toggleMutation.mutate(entry)}
        />
      ),
    },
    {
      title: t("pages.deviceDetail.commands.catalog.actions"),
      key: "actions",
      render: (_: unknown, entry: CommandCatalogEntry) => (
        <Space size={4}>
          <Button
            size="small"
            type="text"
            icon={<EditOutlined />}
            onClick={() => openEdit(entry)}
          />
          <Button
            size="small"
            type="text"
            danger
            icon={<DeleteOutlined />}
            onClick={() =>
              Modal.confirm({
                title: t("pages.deviceDetail.commands.catalog.deleteConfirm", { name: entry.name }),
                okText: t("common.confirm"),
                cancelText: t("common.cancel"),
                okButtonProps: { danger: true },
                onOk: () => deleteMutation.mutateAsync(entry.name),
              })
            }
          />
        </Space>
      ),
    },
  ];

  const stateColor: Record<string, string> = {
    SUCCEEDED: "success",
    DELIVERED: "processing",
    PENDING: "default",
    QUEUED: "default",
    CLAIMED: "default",
    FAILED: "error",
    TIMEOUT: "warning",
    NOT_CONNECTED: "warning",
    EXPIRED: "warning",
  };
  const stateIcon = (s: string) =>
    s === "SUCCEEDED"
      ? <CheckCircleOutlined />
      : s === "FAILED"
        ? <CloseCircleOutlined />
        : <ExclamationCircleOutlined />;

  const historyColumns = [
    { title: t("pages.deviceDetail.commands.history.name"), dataIndex: "name", key: "name" },
    {
      title: t("pages.deviceDetail.commands.history.direction"),
      dataIndex: "direction",
      key: "direction",
      render: (d: string) => d
        ? <Tag color={d === "read" ? "blue" : "orange"}>{t(`pages.deviceDetail.commands.direction${d === "read" ? "Read" : "Write"}`)}</Tag>
        : "-",
    },
    {
      title: t("pages.deviceDetail.commands.history.state"),
      dataIndex: "state",
      key: "state",
      render: (s: string) => <Tag icon={stateIcon(s)} color={stateColor[s]}>{s}</Tag>,
    },
    {
      title: t("pages.deviceDetail.commands.history.time"),
      dataIndex: "created_at",
      key: "created_at",
      render: (v: string) => dayjs(v).format("YYYY-MM-DD HH:mm:ss"),
    },
    {
      title: "",
      key: "open",
      render: (_: unknown, r: CommandRecord) => (
        <Button size="small" type="link" onClick={() => setRecordDrawer(r)}>
          {t("pages.deviceDetail.commands.history.view")}
        </Button>
      ),
    },
  ];

  const enabledEntries = useMemo(
    () => (catalogQ.data ?? []).filter((e) => e.enabled),
    [catalogQ.data],
  );
  // undeclaredPoints lists profile points without an enabled catalog entry,
  // enabling inline declaration from the send area.
  const undeclaredPoints = useMemo(() => {
    const byName = new Map((catalogQ.data ?? []).map((e) => [e.name, e]));
    return points
      .map((p) => ({ point: p, mask: asDirectionMask(p) }))
      .filter(({ point, mask }) =>
        (mask.read || mask.write) && !byName.get(point.measurement_key)?.enabled);
  }, [points, catalogQ.data]);

  const openCreate = useCallback((preset?: CommandCatalogInput) => {
    setEditing(null);
    if (preset) {
      catalogForm.setFieldsValue(preset);
    } else {
      // For a profile-bound device, default the declaration to the first
      // point lacking an enabled catalog entry and carry its read/write
      // mask; manual devices get a blank free-text form.
      const firstUndeclared = undeclaredPoints[0];
      if (firstUndeclared) {
        catalogForm.setFieldsValue(catalogDefaults(
          firstUndeclared.point.measurement_key, firstUndeclared.mask));
      } else {
        catalogForm.setFieldsValue(catalogDefaults("", { read: true, write: false }));
      }
    }
    setCatalogOpen(true);
  }, [catalogForm, undeclaredPoints]);

  const selectedEntry = Form.useWatch("name", sendForm) as string | undefined;
  const selectedCatalog = useMemo(
    () => enabledEntries.find((e) => e.name === selectedEntry) ?? null,
    [enabledEntries, selectedEntry],
  );

  return (
    <Space direction="vertical" size="middle" style={{ width: "100%" }}>
      <Card
        size="small"
        title={t("pages.deviceDetail.commands.catalog.title")}
        extra={
          <Space>
            <FilterSearchBar
              searchValue={catalogSearch}
              onSearchChange={setCatalogSearch}
              searchPlaceholder={t("pages.deviceDetail.commands.catalog.searchPlaceholder")}
            />
            <Button icon={<PlusOutlined />} onClick={() => openCreate()}>
              {t("pages.deviceDetail.commands.catalog.declare")}
            </Button>
          </Space>
        }
      >
        <Spin spinning={catalogQ.isLoading}>
          {catalogQ.error
            ? <Alert type="error" showIcon message={(catalogQ.error as Error).message} />
            : !filteredCatalog.length
              ? <Alert type="info" showIcon message={t("pages.deviceDetail.commands.catalog.empty")} />
              : (
                <Table
                  rowKey="name"
                  size="small"
                  pagination={false}
                  dataSource={filteredCatalog}
                  columns={catalogColumns}
                />
              )}
        </Spin>
      </Card>

      <Card
        size="small"
        title={t("pages.deviceDetail.commands.history.title")}
        extra={
          <Space>
            <Select
              allowClear
              size="small"
              style={{ width: 160 }}
              placeholder={t("pages.deviceDetail.commands.filterState")}
              value={stateFilter}
              onChange={(v) => { setStateFilter(v); setPage(1); }}
              options={Object.keys(stateColor).map((s) => ({ value: s, label: s }))}
            />
            <Button
              type="primary"
              icon={<SendOutlined />}
              disabled={!enabledEntries.length && !profile}
              onClick={() => openSendForPoint()}
            >
              {t("pages.deviceDetail.commands.send")}
            </Button>
          </Space>
        }
      >
        <Spin spinning={historyQ.isLoading}>
          {historyQ.error
            ? <Alert type="error" showIcon message={(historyQ.error as Error).message} />
            : !historyQ.data?.items.length
              ? <Alert type="info" showIcon message={t("pages.deviceDetail.commands.history.empty")} />
              : (
                <Table
                  rowKey="id"
                  size="small"
                  dataSource={historyQ.data?.items ?? []}
                  columns={historyColumns}
                  pagination={{
                    current: page,
                    pageSize,
                    total: historyQ.data?.total ?? 0,
                    showSizeChanger: true,
                    pageSizeOptions: [10, 20, 50],
                    showTotal: (total) => t("common.totalItems", { total }),
                    onChange: (p, ps) => { setPage(p); setPageSize(ps); },
                  }}
                />
              )}
        </Spin>
      </Card>

      <Drawer
        title={editing
          ? t("pages.deviceDetail.commands.catalog.editTitle")
          : t("pages.deviceDetail.commands.catalog.createTitle")}
        open={catalogOpen}
        onClose={() => setCatalogOpen(false)}
        width={460}
        extra={
          <Space>
            <Button onClick={() => setCatalogOpen(false)}>{t("common.cancel")}</Button>
            <Button type="primary" loading={createMutation.isPending || updateMutation.isPending} onClick={onSubmitCatalog}>
              {t("common.save")}
            </Button>
          </Space>
        }
      >
        <Form form={catalogForm} layout="vertical">
          {profile ? (
            <Form.Item
              name="name"
              label={t("pages.deviceDetail.commands.catalog.name")}
              rules={[{ required: true, message: t("pages.deviceDetail.commands.nameRequired") }]}
            >
              <Select
                showSearch
                optionFilterProp="label"
                disabled={!!editing}
                placeholder={t("pages.deviceDetail.commands.pickPoint")}
                options={catalogPointOptions.map((o) => ({
                  value: o.value,
                  label: o.label,
                }))}
                onChange={(value: string) => {
                  const opt = catalogPointOptions.find((o) => o.value === value);
                  if (!opt) return;
                  const mask = asDirectionMask(opt.point);
                  catalogForm.setFieldsValue({
                    allow_read: mask.read,
                    allow_write: mask.write,
                  });
                }}
              />
            </Form.Item>
          ) : (
            <Form.Item
              name="name"
              label={t("pages.deviceDetail.commands.catalog.name")}
              rules={[{ required: true, message: t("pages.deviceDetail.commands.nameRequired") }]}
            >
              <Input disabled={!!editing} placeholder={t("pages.deviceDetail.commands.namePlaceholder")} />
            </Form.Item>
          )}
          <Form.Item name="allow_read" valuePropName="checked" label={t("pages.deviceDetail.commands.directionRead")}>
            <Switch />
          </Form.Item>
          <Form.Item name="allow_write" valuePropName="checked" label={t("pages.deviceDetail.commands.directionWrite")}>
            <Switch />
          </Form.Item>
          <Form.Item
            noStyle
            shouldUpdate={(prev, cur) =>
              prev.allow_read !== cur.allow_read || prev.allow_write !== cur.allow_write}
          >
            {({ getFieldValue }) => (
              <Form.Item
                name="interaction"
                label={t("pages.deviceDetail.commands.catalog.interaction")}
                rules={[{ required: true }]}
              >
                <Select
                  options={[
                    { value: "request-response", label: "request-response" },
                    { value: "one-way", label: "one-way", disabled: !!getFieldValue("allow_read") },
                  ]}
                />
              </Form.Item>
            )}
          </Form.Item>
          <Form.Item name="timeliness" label={t("pages.deviceDetail.commands.catalog.timeliness")} rules={[{ required: true }]}>
            <Select
              options={[
                { value: "realtime", label: "realtime" },
                { value: "persistent", label: "persistent" },
              ]}
            />
          </Form.Item>
          <Form.Item name="default_timeout_ms" label={t("pages.deviceDetail.commands.catalog.timeout")}>
            <InputNumber min={0} step={1000} style={{ width: "100%" }} addonAfter="ms" />
          </Form.Item>
          <Form.Item name="enabled" valuePropName="checked" label={t("pages.deviceDetail.commands.catalog.enabled")}>
            <Switch />
          </Form.Item>
          <Form.Item name="description" label={t("pages.deviceDetail.commands.catalog.description")}>
            <Input.TextArea rows={2} maxLength={512} />
          </Form.Item>
        </Form>
      </Drawer>

      <Drawer
        title={t("pages.deviceDetail.commands.sendTitle")}
        open={sendOpen}
        onClose={() => setSendOpen(false)}
        width={480}
        extra={
          <Button
            type="primary"
            icon={<SendOutlined />}
            loading={sendMutation.isPending}
            onClick={() => sendMutation.mutate()}
          >
            {t("pages.deviceDetail.commands.send")}
          </Button>
        }
      >
        <Form form={sendForm} layout="vertical">
          {profile ? (
            <>
              <Form.Item
                name="name"
                label={t("pages.deviceDetail.commands.catalog.name")}
                rules={[{ required: true, message: t("pages.deviceDetail.commands.nameRequired") }]}
              >
                <Select
                  showSearch
                  optionFilterProp="label"
                  placeholder={t("pages.deviceDetail.commands.pickPoint")}
                  options={enabledEntries.map((e) => {
                    const p = pointByKey.get(e.name);
                    return {
                      value: e.name,
                      label: p
                        ? `${e.name} (${p.read_write || "-"})`
                        : `${e.name} [${e.source}]`,
                    };
                  })}
                />
              </Form.Item>
              {!!undeclaredPoints.length && (
                <Card size="small" type="inner" style={{ marginBottom: 16 }}
                  title={t("pages.deviceDetail.commands.undeclaredTitle")}>
                  <Space wrap>
                    {undeclaredPoints.map(({ point, mask }) => (
                      <Button
                        key={point.measurement_key}
                        size="small"
                        icon={<PlusOutlined />}
                        onClick={() => {
                          setSendOpen(false);
                          openCreate(catalogDefaults(point.measurement_key, mask));
                        }}
                      >
                        {point.measurement_key}
                      </Button>
                    ))}
                  </Space>
                </Card>
              )}
            </>
          ) : (
            <Form.Item
              name="name"
              label={t("pages.deviceDetail.commands.catalog.name")}
              rules={[{ required: true, message: t("pages.deviceDetail.commands.nameRequired") }]}
            >
              <Input placeholder={t("pages.deviceDetail.commands.namePlaceholder")} />
            </Form.Item>
          )}
          <Form.Item
            name="direction"
            label={t("pages.deviceDetail.commands.direction")}
            rules={[{ required: true }]}
          >
            <Radio.Group
              optionType="button"
              buttonStyle="solid"
              onChange={() => undefined}
            >
              <Radio.Button
                value="read"
                disabled={selectedCatalog ? !selectedCatalog.allow_read : false}
              >
                {t("pages.deviceDetail.commands.directionRead")}
              </Radio.Button>
              <Radio.Button
                value="write"
                disabled={selectedCatalog ? !selectedCatalog.allow_write : false}
              >
                {t("pages.deviceDetail.commands.directionWrite")}
              </Radio.Button>
            </Radio.Group>
          </Form.Item>
          <Form.Item
            name="paramsText"
            label={t("pages.deviceDetail.commands.params")}
            tooltip={t("pages.deviceDetail.commands.paramsTooltip")}
            rules={[
              {
                validator: (_, v) => {
                  const text = (v ?? "").trim();
                  if (!text) return Promise.resolve();
                  try {
                    JSON.parse(text);
                    return Promise.resolve();
                  } catch {
                    return Promise.reject(new Error(t("pages.deviceDetail.commands.paramsInvalid")));
                  }
                },
              },
            ]}
          >
            <Input.TextArea rows={5} spellCheck={false} style={{ fontFamily: "monospace" }} />
          </Form.Item>
          <Form.Item name="timeoutMs" label={t("pages.deviceDetail.commands.timeoutOverride")}>
            <InputNumber min={0} step={1000} style={{ width: "100%" }} addonAfter="ms"
              placeholder={selectedCatalog ? String(selectedCatalog.default_timeout_ms || 10000) : "10000"} />
          </Form.Item>
        </Form>
      </Drawer>

      <Drawer
        title={t("pages.deviceDetail.commands.history.recordTitle")}
        open={!!recordDrawer}
        onClose={() => setRecordDrawer(null)}
        width={520}
      >
        {recordDrawer && (
          <Space direction="vertical" style={{ width: "100%" }}>
            <Card size="small">
              <Space wrap>
                <Tag>{recordDrawer.name}</Tag>
                {recordDrawer.direction && (
                  <Tag color={recordDrawer.direction === "read" ? "blue" : "orange"}>
                    {t(`pages.deviceDetail.commands.direction${recordDrawer.direction === "read" ? "Read" : "Write"}`)}
                  </Tag>
                )}
                <Tag color={stateColor[recordDrawer.state]}>{recordDrawer.state}</Tag>
              </Space>
            </Card>
            <Card size="small" title={t("pages.deviceDetail.commands.history.params")}>
              <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
                {recordDrawer.params}
              </pre>
            </Card>
            {recordDrawer.response && (
              <Card size="small" title={t("pages.deviceDetail.commands.history.response")}>
                <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-all" }}>
                  {recordDrawer.response}
                </pre>
              </Card>
            )}
            {recordDrawer.error && (
              <Card size="small" title={t("pages.deviceDetail.commands.history.error")}>
                <pre style={{ margin: 0, whiteSpace: "pre-wrap", wordBreak: "break-all", color: "var(--ant-color-error)" }}>
                  {recordDrawer.error}
                </pre>
              </Card>
            )}
          </Space>
        )}
      </Drawer>
    </Space>
  );
}
