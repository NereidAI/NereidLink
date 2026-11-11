// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// CredentialsTab manages one device's credentials per ADR-0009: list
// with validity window and status, create, enable/disable, rotation
// with overlap, bulk revocation and delete. Secrets are write-only and
// never re-displayed after creation.

import { useMemo, useRef, useState } from "react";
import {
  App as AntApp,
  Alert,
  Button,
  Card,
  DatePicker,
  Descriptions,
  Form,
  Input,
  Drawer,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { DownloadOutlined, PlusOutlined, EyeOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import dayjs, { type Dayjs } from "dayjs";

import {
  createCredential,
  deleteCredential,
  listCredentials,
  revealCredential,
  revokeAllCredentials,
  rotateCredential,
  setCredentialStatus,
  type Credential,
  type CredentialInput,
} from "../../api/credentials";
import { extractError } from "../../api/client";
import { downloadText } from "../../utils/download";
import { deriveCredentialId } from "../../utils/deviceName";
import FilterSearchBar from "../../components/FilterSearchBar";

interface CredentialsTabProps {
  // deviceId is the detailed device.
  deviceId: string;
  // canonicalName is used to derive the default credentials_id
  // (<canonical>_token/_password/_cert/_jwt) in the Add drawer.
  canonicalName?: string;
}

// statusColors keeps lifecycle state visually stable across the app.
const statusColors: Record<string, string> = {
  ACTIVE: "green",
  DISABLED: "default",
  EXPIRED: "red",
};

interface CreateValues {
  type: string;
  credentials_id: string;
  credentials_value?: string;
  x509_source?: "issue" | "paste";
  window?: [Dayjs | null, Dayjs | null];
  issuer?: string;
  subject?: string;
  audience?: string;
  jwks_uri?: string;
}

interface RotateValues {
  credentials_value?: string;
  expires_at?: Dayjs | null;
}

// CredentialsTab renders the credential table and lifecycle drawers.
export default function CredentialsTab({
  deviceId,
  canonicalName,
}: CredentialsTabProps) {
  const { t, i18n } = useTranslation();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const isZh = i18n.language.startsWith("zh");

  // formatTime renders absolute timestamps with the locale pattern
  // mandated by the product (EN 12h clock, zh 24h with slashes).
  const formatTime = (value?: string) =>
    value
      ? dayjs(value).format(isZh ? "YYYY/MM/DD HH:mm:ss" : "M/D/YYYY, h:mm:ss A")
      : t("pages.deviceDetail.credentials.never");

  const [createOpen, setCreateOpen] = useState(false);
  const [rotating, setRotating] = useState<Credential | undefined>();
  const [secretReveal, setSecretReveal] = useState<Credential | undefined>();
  const [revealed, setRevealed] = useState<Credential | undefined>();
  const [issuedMaterial, setIssuedMaterial] = useState<Credential | undefined>();
  const [createForm] = Form.useForm<CreateValues>();
  const [rotateForm] = Form.useForm<RotateValues>();

  // idManuallyEdited tracks whether the operator touched the derived
  // credentials_id; until then, switching credential type re-derives it.
  const idManuallyEdited = useRef(false);

  // createType drives the type-dependent secret field; W3 expands the
  // form around the same watched value.
  const createType = Form.useWatch("type", createForm);

  // x509Source selects built-in CA issuance versus a pasted certificate.
  const x509Source = Form.useWatch("x509_source", createForm);

  // jwksURI switches a JWT credential from an inline verification key to a
  // remote JWK Set; exactly one source is required on submit.
  const jwksURI = Form.useWatch("jwks_uri", createForm);

  const query = useQuery({
    queryKey: ["device-credentials", deviceId],
    queryFn: () => listCredentials(deviceId),
  });
  // Memoized so downstream useMemo dependencies keep a stable identity
  // instead of seeing a fresh [] fallback on every render.
  const credentials = useMemo(() => query.data ?? [], [query.data]);

  const [keyword, setKeyword] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");

  // typeOptions lists only the types actually present, in the canonical
  // order; labels come from new credential type enum keys.
  const typeOptions = useMemo(
    () =>
      (["TOKEN", "PASSWORD", "X509", "JWT"] as const)
        .filter((type) => credentials.some((row) => row.type === type))
        .map((type) => ({
          value: type,
          label: t(`pages.deviceDetail.credentials.types.${type}`),
        })),
    [credentials, t],
  );

  // statusFilterOptions covers the lifecycle states a user filters by;
  // labels reuse the existing credential states keys.
  const statusFilterOptions = (["ACTIVE", "DISABLED"] as const).map(
    (status) => ({
      value: status,
      label: t(`pages.deviceDetail.credentials.states.${status}`),
    }),
  );

  // Filtering is client-side because the list endpoint returns the whole
  // (small) credential set of one device.
  const filteredCredentials = useMemo(() => {
    const term = keyword.trim().toLowerCase();
    return credentials.filter((row) => {
      if (typeFilter && row.type !== typeFilter) return false;
      if (statusFilter && row.status !== statusFilter) return false;
      if (term && !row.credentials_id.toLowerCase().includes(term)) {
        return false;
      }
      return true;
    });
  }, [credentials, keyword, typeFilter, statusFilter]);

  const invalidate = () =>
    queryClient.invalidateQueries({
      queryKey: ["device-credentials", deviceId],
    });

  const createMutation = useMutation({
    mutationFn: async (values: CreateValues) => {
      const input: CredentialInput = {
        type: values.type as CredentialInput["type"],
        credentials_id: values.credentials_id,
      };
      if (values.type === "X509") {
        if (values.x509_source === "issue") {
          // The platform built-in CA generates the key and signs the
          // certificate; no value is pasted.
          input.issue = true;
        } else if (values.credentials_value) {
          input.credentials_value = values.credentials_value;
        }
      } else if (values.credentials_value) {
        input.credentials_value = values.credentials_value;
      }
      if (
        values.type === "JWT" &&
        (values.issuer || values.subject || values.audience || values.jwks_uri)
      ) {
        // JWT constraints are stored as the extra JSONB and enforced by
        // verifyDeviceJWT on every connection; jwks_uri selects remote
        // asymmetric verification instead of the inline key.
        input.extra = {};
        if (values.issuer) input.extra.iss = values.issuer;
        if (values.subject) input.extra.sub = values.subject;
        if (values.audience) input.extra.aud = values.audience;
        if (values.jwks_uri) input.extra.jwks_uri = values.jwks_uri.trim();
      }
      if (values.window?.[0]) {
        input.effective_at = values.window[0].toISOString();
      }
      if (values.window?.[1]) {
        input.expires_at = values.window[1].toISOString();
      }
      return createCredential(deviceId, input);
    },
    onSuccess: async (credential) => {
      await invalidate();
      setCreateOpen(false);
      if (credential.type === "X509" && credential.certificate_pem) {
        // Issued certificate, private key and CA are shown with
        // downloads; the private key stays downloadable via reveal.
        setIssuedMaterial(credential);
      } else if (credential.credentials_value) {
        // Server-generated secret is surfaced in the result drawer.
        setSecretReveal(credential);
      } else {
        message.success(t("pages.deviceDetail.credentials.toasts.created"));
      }
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const statusMutation = useMutation({
    mutationFn: (args: { id: string; status: "ACTIVE" | "DISABLED" }) =>
      setCredentialStatus(deviceId, args.id, args.status),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.deviceDetail.credentials.toasts.statusChanged"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const rotateMutation = useMutation({
    mutationFn: async (args: { id: string; values: RotateValues }) => {
      const input: { credentials_value?: string; expires_at?: string } = {};
      if (args.values.credentials_value) {
        input.credentials_value = args.values.credentials_value;
      }
      if (args.values.expires_at) {
        input.expires_at = args.values.expires_at.toISOString();
      }
      return rotateCredential(deviceId, args.id, input);
    },
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.deviceDetail.credentials.toasts.rotated"));
      setRotating(undefined);
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const revokeMutation = useMutation({
    mutationFn: () => revokeAllCredentials(deviceId),
    onSuccess: async (result) => {
      await invalidate();
      message.success(
        t("pages.deviceDetail.credentials.toasts.revoked", {
          count: result.revoked,
        }),
      );
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const revealMutation = useMutation({
    mutationFn: (id: string) => revealCredential(deviceId, id),
    onSuccess: (credential) => setRevealed(credential),
    onError: (error) => message.error(extractError(error).message),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteCredential(deviceId, id),
    onSuccess: async () => {
      await invalidate();
      message.success(t("pages.deviceDetail.credentials.toasts.deleted"));
    },
    onError: (error) => message.error(extractError(error).message),
  });

  const columns = [
    {
      title: t("pages.deviceDetail.credentials.type"),
      dataIndex: "type",
      width: 110,
      render: (type: string) => <Tag>{type}</Tag>,
    },
    {
      title: t("pages.deviceDetail.credentials.credentialsId"),
      dataIndex: "credentials_id",
      render: (id: string) => <code>{id}</code>,
    },
    {
      title: t("pages.deviceDetail.credentials.status"),
      dataIndex: "status",
      width: 120,
      render: (status: string) => (
        <Tag color={statusColors[status]}>
          {t(`pages.deviceDetail.credentials.states.${status}`, {
            defaultValue: status,
          })}
        </Tag>
      ),
    },
    {
      title: t("pages.deviceDetail.credentials.effectiveAt"),
      dataIndex: "effective_at",
      width: 190,
      render: (value?: string) => formatTime(value),
    },
    {
      title: t("pages.deviceDetail.credentials.expiresAt"),
      dataIndex: "expires_at",
      width: 190,
      render: (value?: string) => formatTime(value),
    },
    {
      title: t("pages.deviceDetail.credentials.actions"),
      key: "actions",
      width: 360,
      render: (_: unknown, row: Credential) => (
        <Space size={4} wrap>
          {row.status === "ACTIVE" && (
            <Button
              size="small"
              onClick={() =>
                statusMutation.mutate({ id: row.id, status: "DISABLED" })
              }
            >
              {t("pages.deviceDetail.credentials.disable")}
            </Button>
          )}
          {row.status === "DISABLED" && (
            <Button
              size="small"
              onClick={() =>
                statusMutation.mutate({ id: row.id, status: "ACTIVE" })
              }
            >
              {t("pages.deviceDetail.credentials.enable")}
            </Button>
          )}
          <Button
            size="small"
            icon={<EyeOutlined />}
            loading={revealMutation.isPending}
            onClick={() => revealMutation.mutate(row.id)}
          >
            {t("pages.deviceDetail.credentials.view")}
          </Button>
          <Button
            size="small"
            onClick={() => {
              setRotating(row);
              rotateForm.resetFields();
            }}
          >
            {t("pages.deviceDetail.credentials.rotate")}
          </Button>
          <Popconfirm
            title={t("pages.deviceDetail.credentials.deleteTitle")}
            onConfirm={() => deleteMutation.mutate(row.id)}
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
        <Space>
          <Popconfirm
            title={t("pages.deviceDetail.credentials.revokeAllTitle")}
            description={t("pages.deviceDetail.credentials.revokeAllDesc")}
            onConfirm={() => revokeMutation.mutate()}
          >
            <Button danger loading={revokeMutation.isPending}>
              {t("pages.deviceDetail.credentials.revokeAll")}
            </Button>
          </Popconfirm>
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              createForm.resetFields();
              idManuallyEdited.current = false;
              setCreateOpen(true);
            }}
          >
            {t("pages.deviceDetail.credentials.add")}
          </Button>
        </Space>
      }
    >
      <FilterSearchBar
        searchValue={keyword}
        onSearchChange={setKeyword}
        searchPlaceholder={t(
          "pages.deviceDetail.credentials.searchPlaceholder",
        )}
        filters={[
          {
            key: "type",
            placeholder: t("pages.deviceDetail.credentials.filterType"),
            value: typeFilter,
            onChange: setTypeFilter,
            options: typeOptions,
          },
          {
            key: "status",
            placeholder: t("pages.deviceDetail.credentials.status"),
            value: statusFilter,
            onChange: setStatusFilter,
            options: statusFilterOptions,
          },
        ]}
      />
      <Table
        rowKey={(row) => row.id}
        loading={query.isFetching}
        columns={columns}
        dataSource={filteredCredentials}
        pagination={{ defaultPageSize: 10, showSizeChanger: true, showTotal: (total) => t("common.totalItems", { total }) }}
        locale={{ emptyText: t("common.noFilterResults") }}
      />
      <Drawer
        open={createOpen}
        title={t("pages.deviceDetail.credentials.addTitle")}
        width={720}
        onClose={() => setCreateOpen(false)}
        footer={null}
        destroyOnHidden
      >
        <Form
          form={createForm}
          layout="vertical"
          preserve={false}
          initialValues={{
            type: "TOKEN",
            x509_source: "issue",
            // Derived here (not via setFieldValue on open) because the
            // drawer unmounts its form (destroyOnHidden + preserve=false);
            // a setFieldValue before mount is discarded, while
            // initialValues is applied when the form remounts on open.
            credentials_id: canonicalName
              ? deriveCredentialId(canonicalName, "TOKEN")
              : undefined,
          }}
          onFinish={(values) => createMutation.mutate(values)}
        >
          <Form.Item
            label={t("pages.deviceDetail.credentials.type")}
            name="type"
            rules={[{ required: true }]}
          >
            <Select
              options={["TOKEN", "PASSWORD", "X509", "JWT"].map((value) => ({
                value,
                label: value,
              }))}
              onChange={(type) => {
                // Re-derive the credentials_id from the new type only while
                // the operator has not customized it.
                if (!idManuallyEdited.current && canonicalName) {
                  createForm.setFieldValue(
                    "credentials_id",
                    deriveCredentialId(canonicalName, type),
                  );
                }
              }}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.deviceDetail.credentials.credentialsId")}
            name="credentials_id"
            extra={t("pages.deviceDetail.credentials.credentialsIdAutoHint")}
            rules={[
              {
                required: true,
                message: t(
                  "pages.deviceDetail.credentials.credentialsIdRequired",
                ),
              },
              {
                // Reject an ID already used by another credential of this
                // device; the global unique constraint still returns 409
                // for collisions across devices.
                validator: (_rule, value: string) =>
                  value &&
                  credentials.some(
                    (row) =>
                      row.credentials_id.toLowerCase() ===
                      String(value).trim().toLowerCase(),
                  )
                    ? Promise.reject(
                        new Error(
                          t(
                            "pages.deviceDetail.credentials.credentialsIdDuplicate",
                          ),
                        ),
                      )
                    : Promise.resolve(),
              },
            ]}
          >
            <Input
              onChange={() => {
                idManuallyEdited.current = true;
              }}
            />
          </Form.Item>
          {createType === "TOKEN" && (
            <Form.Item
              label={t("pages.deviceDetail.credentials.secret")}
              name="credentials_value"
              tooltip={t("pages.deviceDetail.credentials.secretTooltip")}
              extra={t("pages.deviceDetail.credentials.autoHint")}
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          )}
          {createType === "PASSWORD" && (
            <Form.Item
              label={t("pages.deviceDetail.credentials.secret")}
              name="credentials_value"
              rules={[
                {
                  required: true,
                  message: t("pages.deviceDetail.credentials.passwordRequired"),
                },
              ]}
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
          )}
          {createType === "X509" && (
            <>
              <Form.Item
                label={t("pages.deviceDetail.credentials.certificateSource")}
                name="x509_source"
              >
                <Select
                  options={[
                    {
                      value: "issue",
                      label: t("pages.deviceDetail.credentials.certificateIssue"),
                    },
                    {
                      value: "paste",
                      label: t("pages.deviceDetail.credentials.certificatePaste"),
                    },
                  ]}
                />
              </Form.Item>
              {x509Source === "paste" && (
                <Form.Item
                  label={t("pages.deviceDetail.credentials.certificate")}
                  name="credentials_value"
                  rules={[
                    {
                      required: true,
                      message: t("pages.deviceDetail.credentials.certificateRequired"),
                    },
                  ]}
                >
                  <Input.TextArea
                    rows={8}
                    placeholder={t("pages.deviceDetail.credentials.certificatePlaceholder")}
                  />
                </Form.Item>
              )}
              {/* The MQTT username still selects the credential, so an X509
                  device must connect with the credential ID as its username;
                  without it the platform cannot resolve which credential the
                  presented certificate belongs to. */}
              <Alert
                type="info"
                showIcon
                message={t("pages.deviceDetail.credentials.connectHintTitle")}
                description={t("pages.deviceDetail.credentials.x509ConnectHint")}
              />
            </>
          )}
          {createType === "JWT" && (
            <>
              <Form.Item
                label={t("pages.deviceDetail.credentials.jwksUri")}
                name="jwks_uri"
                tooltip={t("pages.deviceDetail.credentials.jwksUriTooltip")}
                rules={[
                  {
                    // Only https (loopback http allowed) when supplied;
                    // mirrors validateJWKSURI on the server.
                    pattern:
                      /^(https:\/\/.+|http:\/\/(localhost|127\.0\.0\.1|\[::1\])(:\d+)?(\/.*)?)$/i,
                    message: t("pages.deviceDetail.credentials.jwksUriInvalid"),
                  },
                ]}
              >
                <Input placeholder="https://issuer.example/.well-known/jwks.json" />
              </Form.Item>
              <Form.Item
                label={t("pages.deviceDetail.credentials.verifyKey")}
                name="credentials_value"
                tooltip={t("pages.deviceDetail.credentials.verifyKeyHint")}
                rules={[
                  {
                    // An inline key is required unless a JWKS URI is set;
                    // remote sets verify asymmetrically by kid.
                    validator: (_, value: string) =>
                      !value && !jwksURI
                        ? Promise.reject(
                            new Error(t("pages.deviceDetail.credentials.verifyKeyOrJwks")),
                          )
                        : Promise.resolve(),
                  },
                ]}
              >
                <Input.TextArea
                  rows={8}
                  placeholder={t("pages.deviceDetail.credentials.verifyKeyPlaceholder")}
                />
              </Form.Item>
              <Form.Item label={t("pages.deviceDetail.credentials.issuer")} name="issuer">
                <Input />
              </Form.Item>
              <Form.Item label={t("pages.deviceDetail.credentials.subject")} name="subject">
                <Input />
              </Form.Item>
              <Form.Item label={t("pages.deviceDetail.credentials.audience")} name="audience">
                <Input />
              </Form.Item>
            </>
          )}
          {(createType !== "X509" || x509Source === "issue") && (
            <Form.Item
              label={t("pages.deviceDetail.credentials.window")}
              name="window"
              tooltip={t("pages.deviceDetail.credentials.windowTooltip")}
              extra={createType === "X509" ? t("pages.deviceDetail.credentials.issueWindowHint") : undefined}
            >
              <DatePicker.RangePicker showTime style={{ width: "100%" }} />
            </Form.Item>
          )}
          <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
            <Space>
              <Button onClick={() => setCreateOpen(false)}>
                {t("common.cancel")}
              </Button>
              <Button
                type="primary"
                htmlType="submit"
                loading={createMutation.isPending}
              >
                {t("common.save")}
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Drawer>
      <Drawer
        open={!!rotating}
        title={t("pages.deviceDetail.credentials.rotateTitle")}
        width={720}
        onClose={() => setRotating(undefined)}
        footer={null}
        destroyOnHidden
      >
        <Form
          form={rotateForm}
          layout="vertical"
          preserve={false}
          onFinish={(values) =>
            rotating && rotateMutation.mutate({ id: rotating.id, values })
          }
        >
          <Form.Item label={t("pages.deviceDetail.credentials.credentialsId")}>
            <Input value={rotating?.credentials_id} disabled />
          </Form.Item>
          <Form.Item
            label={t("pages.deviceDetail.credentials.secret")}
            name="credentials_value"
            tooltip={t("pages.deviceDetail.credentials.rotateSecretTooltip")}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            label={t("pages.deviceDetail.credentials.newExpiresAt")}
            name="expires_at"
          >
            <DatePicker showTime style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
            <Space>
              <Button onClick={() => setRotating(undefined)}>
                {t("common.cancel")}
              </Button>
              <Button
                type="primary"
                htmlType="submit"
                loading={rotateMutation.isPending}
              >
                {t("common.save")}
              </Button>
            </Space>
          </Form.Item>
        </Form>
      </Drawer>
      <Drawer
        open={!!secretReveal}
        title={t("pages.deviceDetail.credentials.secretResultTitle")}
        width={720}
        onClose={() => setSecretReveal(undefined)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button
              type="primary"
              onClick={() => setSecretReveal(undefined)}
            >
              {t("common.done")}
            </Button>
          </Space>
        }
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.deviceDetail.credentials.secretResultWarning")}
        />
        <Descriptions column={1} bordered size="small">
          <Descriptions.Item label={t("pages.deviceDetail.credentials.username")}>
            <Typography.Text copyable code>
              {secretReveal?.credentials_id}
            </Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.deviceDetail.credentials.secretValue")}>
            <Typography.Text copyable={{ text: secretReveal?.credentials_value }} code>
              {secretReveal?.credentials_value}
            </Typography.Text>
            </Descriptions.Item>
        </Descriptions>
      </Drawer>
      <Drawer
        open={!!revealed}
        title={t("pages.deviceDetail.credentials.viewTitle")}
        width={revealed?.type === "X509" || revealed?.type === "JWT" ? 720 : 560}
        onClose={() => setRevealed(undefined)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button type="primary" onClick={() => setRevealed(undefined)}>
              {t("common.done")}
            </Button>
          </Space>
        }
      >
        <Descriptions column={1} bordered size="small">
          <Descriptions.Item label={t("pages.deviceDetail.credentials.username")}>
            <Typography.Text copyable code>
              {revealed?.credentials_id}
            </Typography.Text>
          </Descriptions.Item>
          {revealed?.type === "X509" ? (
            <>
              <Descriptions.Item label={t("pages.deviceDetail.credentials.certificate")}>
                <MaterialBlock
                  value={revealed.certificate_pem ?? ""}
                  onDownload={() =>
                    downloadText(`${revealed.credentials_id}.pem`, revealed.certificate_pem ?? "")
                  }
                  downloadLabel={t("pages.deviceDetail.credentials.downloadCert")}
                />
              </Descriptions.Item>
              <Descriptions.Item label={t("pages.deviceDetail.credentials.privateKey")}>
                {revealed.private_key_pem ? (
                  <MaterialBlock
                    value={revealed.private_key_pem}
                    onDownload={() =>
                      downloadText(
                        `${revealed.credentials_id}-key.pem`,
                        revealed.private_key_pem ?? "",
                      )
                    }
                    downloadLabel={t("pages.deviceDetail.credentials.downloadKey")}
                  />
                ) : (
                  <Typography.Text type="secondary">
                    {t("pages.deviceDetail.credentials.noStoredKey")}
                  </Typography.Text>
                )}
              </Descriptions.Item>
            </>
          ) : revealed?.revealable === false ? (
            <Descriptions.Item label={t("pages.deviceDetail.credentials.secretValue")}>
              <Alert
                type="info"
                showIcon
                message={t("pages.deviceDetail.credentials.notRevealable")}
              />
            </Descriptions.Item>
          ) : (
            <Descriptions.Item label={t("pages.deviceDetail.credentials.secretValue")}>
              <Typography.Paragraph
                copyable={{ text: revealed?.credentials_value }}
                style={{ marginBottom: 0, whiteSpace: "pre-wrap", fontFamily: "monospace" }}
              >
                {revealed?.credentials_value}
              </Typography.Paragraph>
            </Descriptions.Item>
          )}
        </Descriptions>
      </Drawer>
      <Drawer
        open={!!issuedMaterial}
        title={t("pages.deviceDetail.credentials.issuedTitle")}
        width={720}
        onClose={() => setIssuedMaterial(undefined)}
        footer={
          <Space style={{ display: "flex", justifyContent: "flex-end" }}>
            <Button type="primary" onClick={() => setIssuedMaterial(undefined)}>
              {t("common.done")}
            </Button>
          </Space>
        }
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("pages.deviceDetail.credentials.issuedWarning")}
        />
        <Descriptions column={1} bordered size="small">
          <Descriptions.Item label={t("pages.deviceDetail.credentials.username")}>
            <Typography.Text copyable code>
              {issuedMaterial?.credentials_id}
            </Typography.Text>
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.deviceDetail.credentials.certificate")}>
            <MaterialBlock
              value={issuedMaterial?.certificate_pem ?? ""}
              onDownload={() =>
                downloadText(
                  `${issuedMaterial?.credentials_id}.pem`,
                  issuedMaterial?.certificate_pem ?? "",
                )
              }
              downloadLabel={t("pages.deviceDetail.credentials.downloadCert")}
            />
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.deviceDetail.credentials.privateKey")}>
            <MaterialBlock
              value={issuedMaterial?.private_key_pem ?? ""}
              onDownload={() =>
                downloadText(
                  `${issuedMaterial?.credentials_id}-key.pem`,
                  issuedMaterial?.private_key_pem ?? "",
                )
              }
              downloadLabel={t("pages.deviceDetail.credentials.downloadKey")}
            />
          </Descriptions.Item>
          <Descriptions.Item label={t("pages.deviceDetail.credentials.caBundle")}>
            <MaterialBlock
              value={issuedMaterial?.ca_pem ?? ""}
              onDownload={() => downloadText("ca.pem", issuedMaterial?.ca_pem ?? "")}
              downloadLabel={t("pages.deviceDetail.credentials.downloadCA")}
            />
          </Descriptions.Item>
        </Descriptions>
      </Drawer>
    </Card>
  );
}

// MaterialBlockProps configures the shared PEM display/download item.
interface MaterialBlockProps {
  // value is the full PEM text.
  value: string;

  // onDownload triggers the file save.
  onDownload: () => void;

  // downloadLabel is the save button text.
  downloadLabel: string;
}

// MaterialBlock renders multi-line PEM material with copy and download.
function MaterialBlock({ value, onDownload, downloadLabel }: MaterialBlockProps) {
  return (
    <Space direction="vertical" size={4} style={{ width: "100%" }}>
      <Typography.Paragraph
        copyable={{ text: value }}
        style={{ marginBottom: 0, maxHeight: 140, overflow: "auto", whiteSpace: "pre-wrap", fontFamily: "monospace" }}
      >
        {value}
      </Typography.Paragraph>
      <Button size="small" icon={<DownloadOutlined />} onClick={onDownload}>
        {downloadLabel}
      </Button>
    </Space>
  );
}
