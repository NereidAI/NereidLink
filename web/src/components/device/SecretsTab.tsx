// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// SecretsTab renders the gateway's edge secret delivery bindings
// (ADR-0034): deliver a vault secret with generic target selectors (open
// {kind,value} vocabulary the edge alone interprets), watch the
// desired/reported state, and withdraw (the edge erases the copy).
// Bindings update live over the WS stream.

import { useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import {
  Alert,
  AutoComplete,
  Button,
  Drawer,
  Form,
  Input,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Tooltip,
  message,
} from "antd";
import { PlusOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import {
  listSecretBindings,
  deliverSecret,
  withdrawSecret,
  type SecretBinding,
  type BindingSelector,
} from "../../api/secretBindings";
import { listSecrets, type SecretMetadata } from "../../api/secrets";
import { SecretStateColor } from "../../utils/secretState";
import { useWSStore } from "../../store/wsStore";

// selectorKindSuggestions lists the selector kinds offered by the
// type-to-search picker. The vocabulary stays open: operators can type any
// other kind, and the platform never interprets what a kind means.
const selectorKindSuggestions = ["mac", "ip", "serial", "local_id"];

// SecretsTab renders the gateway secret delivery surface.
export default function SecretsTab({
  gatewayId,
  supported,
}: {
  gatewayId: string;
  supported: boolean;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();

  const bindingsQuery = useQuery({
    queryKey: ["gateway-secret-bindings", gatewayId],
    queryFn: () => listSecretBindings(gatewayId),
  });
  const secretsQuery = useQuery({
    queryKey: ["secrets"],
    queryFn: listSecrets,
  });

  const bindings = bindingsQuery.data ?? [];

  const [drawerOpen, setDrawerOpen] = useState(false);
  // editName, when set, means the drawer edits an existing binding and the
  // secret selector is locked; null means delivering a new secret.
  const [editName, setEditName] = useState<string | null>(null);
  // chosenSecret holds the Vault secret Select value as local state so a
  // back-filled value renders regardless of form-field registration.
  const [chosenSecret, setChosenSecret] = useState<string | undefined>(
    undefined,
  );
  // Selector rows are kept in local state (not a nested Form.List) so
  // back-filling a destroyed drawer never depends on field registration.
  const [rows, setRows] = useState<BindingSelector[]>([]);

  const boundNames = useMemo(
    () =>
      new Set(
        (bindingsQuery.data ?? []).map((binding) => binding.secret_name),
      ),
    [bindingsQuery.data],
  );
  // Only typed documents are deliverable; blob secrets have no credential
  // document an edge can parse. When editing, include the secret currently
  // being edited even though it is already bound, so the locked Select can
  // resolve its value.
  const deliverableSecrets = useMemo(
    () =>
      (secretsQuery.data ?? []).filter(
        (secret) =>
          secret.kind === "document" &&
          (!boundNames.has(secret.name) || secret.name === editName),
      ),
    [secretsQuery.data, boundNames, editName],
  );

  // Live updates: subscribe to tenant secret delivery events and refetch
  // when a frame names this gateway. The buffer is only a trigger.
  const subscribeWS = useWSStore((state) => state.subscribe);
  const unsubscribeWS = useWSStore((state) => state.unsubscribe);
  const bindingEvents = useWSStore((state) => state.secretBindingEvents);

  useEffect(() => {
    subscribeWS(`gateway-secrets-${gatewayId}`, "secret_binding", []);
    return () => unsubscribeWS(`gateway-secrets-${gatewayId}`);
  }, [gatewayId, subscribeWS, unsubscribeWS]);

  useEffect(() => {
    if (bindingEvents.length === 0) return;
    const latest = bindingEvents[bindingEvents.length - 1];
    if (latest.gateway_id === gatewayId) {
      void queryClient.invalidateQueries({
        queryKey: ["gateway-secret-bindings", gatewayId],
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [bindingEvents, gatewayId]);

  // openDeliver starts the create drawer.
  const openDeliver = () => {
    setEditName(null);
    setChosenSecret(undefined);
    setRows([]);
    setDrawerOpen(true);
  };

  // openEdit loads the binding's secret and selectors, regardless of kind.
  const openEdit = (binding: SecretBinding) => {
    setEditName(binding.secret_name);
    setChosenSecret(binding.secret_name);
    setRows(binding.selectors.map((s) => ({ kind: s.kind, value: s.value })));
    setDrawerOpen(true);
  };

  // patchRow updates one field of the selector row at index.
  const patchRow = (index: number, change: Partial<BindingSelector>) => {
    setRows(rows.map((row, i) => (i === index ? { ...row, ...change } : row)));
  };

  const deliverMutation = useMutation({
    mutationFn: (input: {
      secretName: string;
      selectors: BindingSelector[];
    }) => deliverSecret(gatewayId, input.secretName, input.selectors),
    onSuccess: () => {
      message.success(t("pages.deviceDetail.secrets.deliverSuccess"));
      setDrawerOpen(false);
      void queryClient.invalidateQueries({
        queryKey: ["gateway-secret-bindings", gatewayId],
      });
    },
    onError: (error: unknown) => {
      message.error(error instanceof Error ? error.message : String(error));
    },
  });

  const submit = () => {
    const secretName = editName ?? chosenSecret ?? "";
    if (!secretName) {
      message.error(t("pages.deviceDetail.secrets.form.secretRequired"));
      return;
    }
    const selectors: BindingSelector[] = rows
      .map((row) => ({ kind: row.kind.trim(), value: row.value.trim() }))
      .filter((row) => row.kind !== "" && row.value !== "");
    deliverMutation.mutate({ secretName, selectors });
  };

  const withdrawMutation = useMutation({
    mutationFn: (secretName: string) => withdrawSecret(gatewayId, secretName),
    onSuccess: () => {
      message.success(t("pages.deviceDetail.secrets.withdrawSuccess"));
      void queryClient.invalidateQueries({
        queryKey: ["gateway-secret-bindings", gatewayId],
      });
    },
    onError: (error: unknown) => {
      message.error(error instanceof Error ? error.message : String(error));
    },
  });

  const columns = [
    {
      title: t("pages.deviceDetail.secrets.columns.secret"),
      dataIndex: "secret_name",
      render: (name: string) => <strong>{name}</strong>,
    },
    {
      title: t("pages.deviceDetail.secrets.columns.type"),
      render: (_: unknown, binding: SecretBinding) =>
        binding.secret_type ? (
          <Tag color="geekblue">
            {binding.secret_type}
            {binding.type_version ? `:${binding.type_version}` : ""}
          </Tag>
        ) : (
          "-"
        ),
    },
    {
      title: t("pages.deviceDetail.secrets.columns.targets"),
      render: (_: unknown, binding: SecretBinding) =>
        binding.selectors.length > 0 ? (
          <Space size={[4, 4]} wrap>
            {binding.selectors.map((selector) => (
              <Tag key={`${selector.kind}:${selector.value}`}>
                {selector.kind}: {selector.value}
              </Tag>
            ))}
          </Space>
        ) : (
          <Tag>{t("pages.deviceDetail.secrets.noTargets")}</Tag>
        ),
    },
    {
      title: t("pages.deviceDetail.secrets.columns.status"),
      render: (_: unknown, binding: SecretBinding) => {
        const tag = (
          <Tag color={SecretStateColor[binding.status] ?? "default"}>
            {t(`pages.deviceDetail.secrets.states.${binding.status}`, {
              defaultValue: binding.status,
            })}
          </Tag>
        );
        return binding.reason ? (
          <Tooltip title={binding.reason}>{tag}</Tooltip>
        ) : (
          tag
        );
      },
    },
    {
      title: t("pages.deviceDetail.secrets.columns.revision"),
      dataIndex: "revision",
    },
    {
      title: t("common.actions"),
      render: (_: unknown, binding: SecretBinding) => (
        <Space>
          <Button
            type="link"
            size="small"
            disabled={binding.status === "REMOVING"}
            onClick={() => openEdit(binding)}
          >
            {t("common.edit")}
          </Button>
          <Popconfirm
            title={t("pages.deviceDetail.secrets.withdrawConfirm")}
            okText={t("common.confirm")}
            cancelText={t("common.cancel")}
            okButtonProps={{ danger: true }}
            onConfirm={() => withdrawMutation.mutate(binding.secret_name)}
          >
            <Button type="link" size="small" danger>
              {t("pages.deviceDetail.secrets.withdraw")}
            </Button>
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <>
      {!supported && (
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.deviceDetail.secrets.unsupported.title")}
          description={t("pages.deviceDetail.secrets.unsupported.description")}
        />
      )}
      <Space style={{ marginBottom: 12 }}>
        <Button type="primary" icon={<PlusOutlined />} onClick={openDeliver}>
          {t("pages.deviceDetail.secrets.deliver")}
        </Button>
      </Space>
      <Table
        rowKey="id"
        loading={bindingsQuery.isLoading}
        columns={columns}
        dataSource={bindings}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("pages.deviceDetail.secrets.empty") }}
      />
      <Drawer
        width={640}
        open={drawerOpen}
        onClose={() => setDrawerOpen(false)}
        title={
          editName
            ? t("pages.deviceDetail.secrets.editTitle")
            : t("pages.deviceDetail.secrets.deliverTitle")
        }
        destroyOnHidden
        extra={
          <Space>
            <Button onClick={() => setDrawerOpen(false)}>
              {t("common.cancel")}
            </Button>
            <Button
              type="primary"
              onClick={submit}
              loading={deliverMutation.isPending}
            >
              {t("common.save")}
            </Button>
          </Space>
        }
      >
        <Form layout="vertical">
          <Form.Item label={t("pages.deviceDetail.secrets.form.secret")}>
            <Select
              value={chosenSecret}
              disabled={editName !== null}
              placeholder={t("pages.deviceDetail.secrets.form.secretPlaceholder")}
              options={deliverableSecrets.map((secret: SecretMetadata) => ({
                label: secret.name,
                value: secret.name,
              }))}
              showSearch
              optionFilterProp="label"
              onChange={(value) => setChosenSecret(value)}
            />
          </Form.Item>
          <Space style={{ marginBottom: 8 }}>
            <span>{t("pages.deviceDetail.secrets.form.targets")}</span>
            <Button size="small" onClick={() => setRows([...rows, { kind: "", value: "" }])}>
              {t("pages.deviceDetail.secrets.form.addTarget")}
            </Button>
          </Space>
          {rows.map((row, index) => (
            <Space key={index} align="baseline" style={{ display: "flex" }}>
              <AutoComplete
                style={{ width: 150 }}
                value={row.kind}
                options={selectorKindSuggestions.map((kind) => ({
                  label: kind,
                  value: kind,
                }))}
                placeholder={t(
                  "pages.deviceDetail.secrets.form.kindPlaceholder",
                )}
                filterOption={(input, option) =>
                  (option?.value ?? "")
                    .toLowerCase()
                    .includes(input.toLowerCase())
                }
                onChange={(kind) => patchRow(index, { kind })}
              />
              <Input
                style={{ flex: 1 }}
                value={row.value}
                placeholder={t(
                  "pages.deviceDetail.secrets.form.valuePlaceholder",
                )}
                onChange={(event) =>
                  patchRow(index, { value: event.target.value })
                }
              />
              <Button
                type="link"
                danger
                size="small"
                onClick={() => setRows(rows.filter((_, i) => i !== index))}
              >
                {t("common.delete")}
              </Button>
            </Space>
          ))}
        </Form>
      </Drawer>
    </>
  );
}
