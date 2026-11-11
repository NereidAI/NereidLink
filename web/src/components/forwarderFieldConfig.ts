// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// forwarderFieldConfig.ts holds the declarative per-sink field descriptors,
// the sink-type options and the list-view target summary for the forwarder
// editor (ADR-0018). It contains no components, so the editor's Fast Refresh
// boundary stays clean; SinkConfigField lives in forwarderFields.tsx.

import type { FormInstance } from "antd";

export type FieldType =
  | "text"
  | "password"
  | "textarea"
  | "number"
  | "select"
  | "switch";

// SelectOption is one literal select choice.
interface SelectOption {
  // value is stored in sink_config.
  value: string | number;
  // label is shown; protocol identifiers are shown verbatim.
  label?: string;
  // labelKey is the i18n key for a localized label.
  labelKey?: string;
}

// SinkField describes one sink_config field.
export interface SinkField {
  // key is the sink_config property name.
  key: string;
  // type selects the control.
  type: FieldType;
  // labelKey is the i18n key of the label.
  labelKey: string;
  // hintKey is the optional i18n key of the tooltip.
  hintKey?: string;
  // required marks the field mandatory.
  required?: boolean;
  // requiredIf marks the field mandatory only when another cfg field is
  // truthy (e.g. username required when use_auth is on).
  requiredIf?: string;
  // showIf renders the field only when another cfg field is truthy.
  showIf?: string;
  // placeholder is the literal (non-translated) example text, if any.
  placeholder?: string;
  // min/max bound a number field.
  min?: number;
  max?: number;
  // rows sizes a textarea.
  rows?: number;
  // options populate a select.
  options?: SelectOption[];
  // afterSelect runs after a select choice is picked; used to cascade
  // defaults into other cfg fields (e.g. security mode sets the port).
  afterSelect?: (
    form: FormInstance,
    value: string | number,
  ) => void;
}

// tPrefix is the i18n prefix shared by field labels/hints.
const p = "pages.forwarders.fields";

// sinkFields maps every supported sink type to its configuration fields. The
// sets mirror ThingsBoard where applicable but only expose keys the egress
// sinks currently consume, so a created forwarder validates and delivers.
export const sinkFields: Record<string, SinkField[]> = {
  http: [
    {
      key: "url", type: "text", required: true,
      labelKey: `${p}.url`, placeholder: "https://example.com/webhook",
    },
    {
      key: "method", type: "select", labelKey: `${p}.method`,
      options: ["GET", "POST", "PUT", "PATCH", "DELETE"].map((m) => ({
        value: m, label: m,
      })),
    },
    { key: "username", type: "text", labelKey: `${p}.username` },
    { key: "password", type: "password", labelKey: `${p}.password` },
    {
      key: "headers", type: "textarea", rows: 4,
      labelKey: `${p}.headers`, hintKey: `${p}.headersHint`,
      placeholder: '{\n  "Authorization": "Bearer ..."\n}',
    },
    {
      key: "ca_cert", type: "textarea", rows: 4,
      labelKey: `${p}.caCert`, hintKey: `${p}.caCertHint`,
      placeholder: "-----BEGIN CERTIFICATE-----",
    },
    {
      key: "client_cert", type: "textarea", rows: 4,
      labelKey: `${p}.clientCert`, hintKey: `${p}.clientCertHint`,
    },
    {
      key: "client_key", type: "textarea", rows: 4,
      labelKey: `${p}.clientKey`, hintKey: `${p}.clientKeyHint`,
    },
    {
      key: "skip_tls_verify", type: "switch",
      labelKey: `${p}.skipTLSVerify`, hintKey: `${p}.skipTLSVerifyHint`,
    },
  ],

  mqtt: [
    {
      key: "broker", type: "text", required: true,
      labelKey: `${p}.broker`, placeholder: "tcp://host:1883",
      hintKey: `${p}.brokerHint`,
    },
    { key: "topic", type: "text", required: true, labelKey: `${p}.topic` },
    {
      key: "qos", type: "select", labelKey: `${p}.qos`,
      options: [0, 1, 2].map((q) => ({ value: q, label: String(q) })),
    },
    { key: "client_id", type: "text", labelKey: `${p}.clientId` },
    { key: "username", type: "text", labelKey: `${p}.username` },
    { key: "password", type: "password", labelKey: `${p}.password` },
  ],

  nats: [
    {
      key: "servers", type: "text", required: true,
      labelKey: `${p}.servers`, placeholder: "nats://host:4222",
    },
    { key: "subject", type: "text", required: true, labelKey: `${p}.subject` },
    { key: "username", type: "text", labelKey: `${p}.username` },
    { key: "password", type: "password", labelKey: `${p}.password` },
    { key: "token", type: "password", labelKey: `${p}.token` },
  ],

  amqp: [
    {
      key: "url", type: "text", required: true,
      labelKey: `${p}.url`, placeholder: "amqp://user:pass@host:5672/vhost",
    },
    {
      key: "exchange", type: "text", labelKey: `${p}.exchange`,
      hintKey: `${p}.exchangeHint`,
    },
    { key: "routing_key", type: "text", required: true, labelKey: `${p}.routingKey` },
  ],

  kafka: [
    {
      key: "brokers", type: "text", required: true,
      labelKey: `${p}.brokers`, placeholder: "host:9092,host2:9092",
      hintKey: `${p}.brokersHint`,
    },
    { key: "topic", type: "text", required: true, labelKey: `${p}.topic` },
    {
      key: "use_sasl", type: "switch",
      labelKey: `${p}.useSASL`, hintKey: `${p}.useSASLHint`,
    },
    {
      key: "username", type: "text", requiredIf: "use_sasl", showIf: "use_sasl",
      labelKey: `${p}.username`, hintKey: `${p}.saslUsernameHint`,
      placeholder: "nluser",
    },
    {
      key: "password", type: "password", showIf: "use_sasl",
      labelKey: `${p}.password`, hintKey: `${p}.saslPasswordHint`,
    },
  ],

  email: [
    { key: "host", type: "text", required: true, labelKey: `${p}.host` },
    {
      key: "security", type: "select", labelKey: `${p}.security`,
      hintKey: `${p}.securityHint`,
      options: [
        { value: "starttls", labelKey: `${p}.securityStarttls` },
        { value: "tls", labelKey: `${p}.securityTls` },
        { value: "none", labelKey: `${p}.securityNone` },
      ],
      afterSelect: (form, mode) => {
        const defaultPorts: Record<string, number> = {
          starttls: 587, tls: 465, none: 25,
        };
        const port = defaultPorts[String(mode)];
        if (port !== undefined) form.setFieldValue(["cfg", "port"], port);
      },
    },
    {
      key: "port", type: "number", labelKey: `${p}.port`,
      hintKey: `${p}.portHint`, min: 1, max: 65535,
    },
    { key: "from", type: "text", required: true, labelKey: `${p}.from` },
    {
      key: "to", type: "text", required: true,
      labelKey: `${p}.to`, hintKey: `${p}.toHint`,
      placeholder: "ops@example.com,team@example.com",
    },
    { key: "subject", type: "text", labelKey: `${p}.subject` },
    {
      key: "ehlo_domain", type: "text",
      labelKey: `${p}.ehloDomain`, hintKey: `${p}.ehloDomainHint`,
      placeholder: "mail.example.com",
    },
    {
      key: "use_auth", type: "switch",
      labelKey: `${p}.useAuth`, hintKey: `${p}.useAuthHint`,
    },
    {
      key: "username", type: "text", requiredIf: "use_auth",
      labelKey: `${p}.username`,
    },
    { key: "password", type: "password", labelKey: `${p}.password` },
  ],

  sms: [
    {
      key: "url", type: "text", required: true,
      labelKey: `${p}.url`, placeholder: "https://sms-provider.example/send",
    },
    { key: "to", type: "text", required: true, labelKey: `${p}.to` },
    {
      key: "number_field", type: "text", labelKey: `${p}.numberField`,
      hintKey: `${p}.numberFieldHint`,
    },
    {
      key: "message_field", type: "text", labelKey: `${p}.messageField`,
      hintKey: `${p}.messageFieldHint`,
    },
    {
      key: "headers", type: "textarea", rows: 4,
      labelKey: `${p}.headers`, hintKey: `${p}.headersHint`,
    },
  ],
};

// sinkTypeOptions lists every selectable sink for the sink-type dropdown.
export function sinkTypeOptions(t: (k: string) => string) {
  return Object.keys(sinkFields).map((kind) => ({
    value: kind,
    label: t(`pages.forwarders.sink.${kind}`),
  }));
}

// asText renders a config scalar as a string, joining an array with commas.
function asText(value: unknown): string {
  if (Array.isArray(value)) return value.join(", ");
  return value === undefined || value === null ? "" : String(value);
}

// joinTarget combines an endpoint and a destination (topic/subject/...) with
// an arrow, omitting empty pieces.
function joinTarget(endpoint: string, destination: string): string {
  return [endpoint, destination].filter(Boolean).join(" \u2192 ");
}

// forwarderTarget summarizes the delivery target for the list view. Only
// HTTP-like sinks (webhook and the SMS HTTP provider) carry an HTTP method;
// broker sinks return an empty method so the table does not mislabel them
// as POST.
export function forwarderTarget(
  type: string,
  cfg: Record<string, unknown>,
): { method: string | null; target: string } {
  switch (type) {
    case "http":
    case "sms":
      return {
        method: asText(cfg.method) || "POST",
        target: asText(cfg.url),
      };
    case "mqtt":
      return { method: null, target: joinTarget(asText(cfg.broker), asText(cfg.topic)) };
    case "nats":
      return { method: null, target: joinTarget(asText(cfg.servers), asText(cfg.subject)) };
    case "amqp":
      return { method: null, target: joinTarget(asText(cfg.url), asText(cfg.routing_key)) };
    case "kafka":
      return { method: null, target: joinTarget(asText(cfg.brokers), asText(cfg.topic)) };
    case "email": {
      const host = asText(cfg.host);
      const port = asText(cfg.port);
      const endpoint = host ? `${host}${port ? `:${port}` : ""}` : "";
      return { method: null, target: joinTarget(endpoint, asText(cfg.to)) };
    }
    default:
      return { method: null, target: asText(cfg.url) };
  }
}
