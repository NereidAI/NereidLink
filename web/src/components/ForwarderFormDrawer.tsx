// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// ForwarderFormDrawer is the create/edit drawer for third-party data
// forwarders (ADR-0018). Common forwarder settings live at the top; the
// sink-specific section is rendered generically from the descriptor in
// forwarderFields based on the chosen sink type and collected under the "cfg"
// form prefix, which is mapped to sink_config on submit.

import {
  App as AntApp,
  Button,
  Divider,
  Drawer,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
} from "antd";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";

import {
  createForwarder,
  updateForwarder,
  type Forwarder,
} from "../api/forwarders";
import { extractError } from "../api/client";
import { SinkConfigField } from "./forwarderFields";
import { sinkFields, sinkTypeOptions } from "./forwarderFieldConfig";

interface ForwarderFormDrawerProps {
  // open controls visibility.
  open: boolean;
  // forwarder is present when editing.
  forwarder?: Forwarder;
  // onClose hides the drawer without saving.
  onClose: () => void;
}

// FormCfgValue is the primitive-only shape held by the form: decodeConfig
// renders maps and lists as editable text, so nested objects never live in
// the form state. encodeConfig parses the text back into structured values.
type FormCfgValue = string | number | boolean;

// FormValues mirrors the form. cfg is the sink-specific section mapped to
// sink_config on submit.
interface FormValues {
  name: string;
  enabled: boolean;
  sink_type: string;
  max_attempts: number;
  message_ttl_seconds: number;
  max_buffer_messages: number;
  cfg: Record<string, FormCfgValue>;
}

// ttlSecondsOptions are the offered message lifetimes. Short values keep a
// recovering receiver from getting the whole outage window.
const ttlSecondsOptions = [
  60, 300, 600, 1800, 3600, 7200, 21600, 86400,
].map((seconds) => ({ value: seconds, label: ttlLabel(seconds) }));

// ttlLabel renders a human-readable TTL option.
function ttlLabel(seconds: number): string {
  if (seconds < 3600) return `${seconds / 60} min`;
  if (seconds === 3600) return "1 hour";
  if (seconds < 86400) return `${seconds / 3600} hours`;
  return `${seconds / 86400} day`;
}

// ForwarderFormDrawer renders the forwarder editor.
export default function ForwarderFormDrawer({
  open,
  forwarder,
  onClose,
}: ForwarderFormDrawerProps) {
  const { t } = useTranslation();
  const [form] = Form.useForm<FormValues>();
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  // watchedType follows the Select while creating. While editing the field
  // is disabled, so the stored type from the forwarder is used as the
  // render fallback: the right sink fields must be mounted on the FIRST
  // open render, before any watch update, or nested values set in
  // afterOpenChange would be dropped (preserve={false} discards values for
  // fields that are not mounted).
  const watchedType = Form.useWatch("sink_type", form);
  const sinkType = watchedType ?? forwarder?.sink_type;

  const mutation = useMutation({
    mutationFn: async (values: FormValues) => {
      // Sink type is fixed after creation; always send the stored value on
      // edit even if the disabled field was tampered with in the DOM.
      const sinkType = forwarder?.sink_type ?? values.sink_type;
      const input = {
        name: values.name,
        sink_type: sinkType,
        sink_config: encodeConfig(sinkType, values.cfg ?? {}),
        encoder: "json",
        max_attempts: values.max_attempts,
        message_ttl_ms: values.message_ttl_seconds * 1000,
        max_buffer_messages: values.max_buffer_messages,
        enabled: values.enabled,
      };
      return forwarder
        ? updateForwarder(forwarder.id, input)
        : createForwarder(input);
    },
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["forwarders"] });
      message.success(
        forwarder
          ? t("pages.forwarders.toasts.updated")
          : t("pages.forwarders.toasts.created"),
      );
      onClose();
    },
    onError: (error) => message.error(extractError(error).message),
  });

  return (
    <Drawer
      open={open}
      width={640}
      title={
        forwarder
          ? t("pages.forwarders.form.editTitle")
          : t("pages.forwarders.form.createTitle")
      }
      onClose={onClose}
      footer={null}
      destroyOnHidden
      afterOpenChange={(visible) => {
        if (visible) {
          const type = forwarder?.sink_type ?? "http";
          form.setFieldsValue({
            name: forwarder?.name ?? "",
            enabled: forwarder?.enabled ?? true,
            sink_type: type,
            max_attempts: forwarder?.max_attempts ?? 5,
            message_ttl_seconds:
              Math.round((forwarder?.message_ttl_ms ?? 3_600_000) / 1000) ||
              3600,
            max_buffer_messages: forwarder?.max_buffer_messages ?? 1000,
            cfg: decodeConfig(type, forwarder?.sink_config ?? {}),
          });
        }
      }}
    >
      <Form
        form={form}
        layout="vertical"
        preserve={false}
        onFinish={(values) => mutation.mutate(values)}
      >
        <Form.Item
          label={t("common.name")}
          name="name"
          rules={[
            { required: true, message: t("pages.forwarders.form.nameRequired") },
          ]}
        >
          <Input placeholder={t("pages.forwarders.form.namePlaceholder")} />
        </Form.Item>

        <Space wrap>
          <Form.Item label={t("common.enabled")} name="enabled" valuePropName="checked">
            <Switch />
          </Form.Item>
          <Form.Item label={t("pages.forwarders.form.sinkType")} name="sink_type"
            tooltip={forwarder ? t("pages.forwarders.form.sinkTypeFixedHint") : undefined}>
            <Select
              style={{ width: 180 }}
              options={sinkTypeOptions(t)}
              disabled={!!forwarder}
            />
          </Form.Item>
          <Form.Item
            label={t("pages.forwarders.form.maxAttempts")}
            name="max_attempts"
            tooltip={t("pages.forwarders.form.maxAttemptsHint")}
          >
            <InputNumber min={1} max={50} style={{ width: 110 }} />
          </Form.Item>
        </Space>

        <Space wrap align="end">
          <Form.Item
            label={t("pages.forwarders.form.messageTTL")}
            name="message_ttl_seconds"
            tooltip={t("pages.forwarders.form.messageTTLHint")}
          >
            <Select style={{ width: 150 }} options={ttlSecondsOptions} />
          </Form.Item>
          <Form.Item
            label={t("pages.forwarders.form.maxBuffer")}
            name="max_buffer_messages"
            tooltip={t("pages.forwarders.form.maxBufferHint")}
          >
            <InputNumber min={1} max={1_000_000} style={{ width: 140 }} />
          </Form.Item>
        </Space>

        <Divider plain style={{ fontSize: 13 }}>
          {t(`pages.forwarders.sink.${sinkType ?? "http"}`)}
        </Divider>

        {(sinkFields[sinkType ?? "http"] ?? []).map((field) => (
          <SinkConfigField key={field.key} field={field} t={t} />
        ))}

        <Form.Item style={{ marginBottom: 0, textAlign: "right" }}>
          <Space>
            <Button onClick={onClose}>{t("common.cancel")}</Button>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>
              {t("common.save")}
            </Button>
          </Space>
        </Form.Item>
      </Form>
    </Drawer>
  );
}

// asString reads a stored sink_config value as string.
function asString(value: unknown): string {
  if (value === null || value === undefined) return "";
  return String(value);
}

// objectToText renders a stored map/string value as editable JSON text.
function objectToText(value: unknown): string {
  if (value === null || value === undefined || value === "") return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

// textToObject parses a JSON text field into an object, defaulting to {}.
function textToObject(value: unknown): Record<string, unknown> {
  const raw = asString(value).trim();
  if (!raw) return {};
  return JSON.parse(raw) as Record<string, unknown>;
}

// csvToList splits comma-separated text into a trimmed, non-empty list.
function csvToList(value: unknown): string[] {
  return asString(value)
    .split(",")
    .map((item) => item.trim())
    .filter((item) => item !== "");
}

// listToCSV renders a stored list/string as comma-separated text.
function listToCSV(value: unknown): string {
  if (Array.isArray(value)) return value.join(", ");
  return asString(value);
}

// normalizeEmailSecurity resolves the stored transport mode, defaulting
// to STARTTLS for forwarders created before the switch existed.
function normalizeEmailSecurity(value: string): string {
  return value === "tls" || value === "none" ? value : "starttls";
}

// defaultEmailPort returns the conventional port for a security mode.
function defaultEmailPort(security: string): number {
  if (security === "tls") return 465;
  if (security === "none") return 25;
  return 587;
}

// decodeConfig converts stored sink_config into form-friendly values, applying
// per-sink defaults and rendering maps/lists as editable text.
function decodeConfig(
  type: string,
  stored: Record<string, unknown>,
): Record<string, FormCfgValue> {
  const get = (key: string) => stored[key];
  switch (type) {
    case "http":
      return {
        url: asString(get("url")),
        method: asString(get("method")) || "POST",
        username: asString(get("username")),
        password: asString(get("password")),
        headers: objectToText(get("headers")),
        ca_cert: asString(get("ca_cert")),
        client_cert: asString(get("client_cert")),
        client_key: asString(get("client_key")),
        skip_tls_verify: get("skip_tls_verify") === true,
      };
    case "mqtt": {
      const qos = get("qos");
      return {
        broker: asString(get("broker")),
        topic: asString(get("topic")),
        qos: typeof qos === "number" ? qos : 0,
        client_id: asString(get("client_id")),
        username: asString(get("username")),
        password: asString(get("password")),
      };
    }
    case "nats":
      return {
        servers: asString(get("servers")),
        subject: asString(get("subject")),
        username: asString(get("username")),
        password: asString(get("password")),
        token: asString(get("token")),
      };
    case "amqp":
      return {
        url: asString(get("url")),
        exchange: asString(get("exchange")),
        routing_key: asString(get("routing_key")),
      };
    case "kafka":
      return {
        brokers: Array.isArray(get("brokers"))
          ? listToCSV(get("brokers"))
          : asString(get("brokers")),
        topic: asString(get("topic")),
        use_sasl: get("use_sasl") === true || asString(get("username")) !== "",
        username: asString(get("username")),
        password: asString(get("password")),
      };
    case "email": {
      const security = normalizeEmailSecurity(asString(get("security")));
      const port = get("port");
      return {
        host: asString(get("host")),
        security,
        port: typeof port === "number" ? port : defaultEmailPort(security),
        from: asString(get("from")),
        to: listToCSV(get("to")),
        subject: asString(get("subject")),
        ehlo_domain: asString(get("ehlo_domain")),
        use_auth: get("use_auth") === true,
        username: asString(get("username")),
        password: asString(get("password")),
      };
    }
    case "sms":
      return {
        url: asString(get("url")),
        to: asString(get("to")),
        number_field: asString(get("number_field")) || "to",
        message_field: asString(get("message_field")) || "text",
        headers: objectToText(get("headers")),
      };
    default:
      return {};
  }
}

// encodeConfig converts form values back to stored sink_config, parsing JSON
// text and comma lists into the shapes the egress sinks consume.
function encodeConfig(
  type: string,
  cfg: Record<string, unknown>,
): Record<string, unknown> {
  const get = (key: string) => cfg[key];
  switch (type) {
    case "http":
      return {
        url: asString(get("url")),
        method: asString(get("method")).toUpperCase() || "POST",
        username: asString(get("username")),
        password: asString(get("password")),
        headers: textToObject(get("headers")),
        ca_cert: asString(get("ca_cert")),
        client_cert: asString(get("client_cert")),
        client_key: asString(get("client_key")),
        skip_tls_verify: get("skip_tls_verify") === true,
      };
    case "mqtt":
      return {
        broker: asString(get("broker")),
        topic: asString(get("topic")),
        qos: Number(get("qos")) || 0,
        client_id: asString(get("client_id")),
        username: asString(get("username")),
        password: asString(get("password")),
      };
    case "nats":
      return {
        servers: asString(get("servers")),
        subject: asString(get("subject")),
        username: asString(get("username")),
        password: asString(get("password")),
        token: asString(get("token")),
      };
    case "amqp":
      return {
        url: asString(get("url")),
        exchange: asString(get("exchange")),
        routing_key: asString(get("routing_key")),
      };
    case "kafka": {
      const useSASL = get("use_sasl") === true;
      return {
        brokers: asString(get("brokers")),
        topic: asString(get("topic")),
        use_sasl: useSASL,
        username: useSASL ? asString(get("username")) : "",
        password: useSASL ? asString(get("password")) : "",
      };
    }
    case "email": {
      const security = normalizeEmailSecurity(asString(get("security")));
      return {
        host: asString(get("host")),
        security,
        port: Number(get("port")) || defaultEmailPort(security),
        from: asString(get("from")),
        to: csvToList(get("to")),
        subject: asString(get("subject")),
        ehlo_domain: asString(get("ehlo_domain")),
        use_auth: get("use_auth") === true,
        username: asString(get("username")),
        password: asString(get("password")),
      };
    }
    case "sms":
      return {
        url: asString(get("url")),
        to: asString(get("to")),
        number_field: asString(get("number_field")) || "to",
        message_field: asString(get("message_field")) || "text",
        headers: textToObject(get("headers")),
      };
    default:
      return {};
  }
}
