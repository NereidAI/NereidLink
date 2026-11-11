<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Data Forwarders

A data forwarder sends device data to an external system (such as a webhook or message broker) without making that system part of the telemetry path. Forwarding is fully asynchronous: a rule-chain **forward** node publishes the message to a bounded queue, and a worker delivers it in the background. A failing receiver never blocks ingestion or rule processing.

## Forwarders tab

Each row is one forwarder.

- **Name** — unique display name within the tenant.
- **Sink** — the delivery mechanism: HTTP webhook, MQTT broker, NATS server, RabbitMQ (AMQP), Apache Kafka, email (SMTP) or SMS (HTTP provider).
- **Target** — the sink destination, for example the HTTP method and endpoint URL.
- **Max attempts** — delivery retries before the message is moved to dead letters.
- **Enabled** — turns delivery on or off.

Create or edit a forwarder in the drawer. Every forwarder shares the settings below; the remaining fields depend on the selected sink type.

## Common settings

- **Sink type** — chosen only when creating a forwarder. It cannot be changed afterward: the stored configuration is specific to one sink type, so create a new forwarder if you need a different delivery mechanism.
- **Max attempts** — delivery retries before the message is moved to dead letters.
- **Message lifetime** — how long an undelivered message is kept. Messages older than this are dropped even if the receiver is still down, so a recovered receiver is not flooded with the whole outage. The default is one hour.
- **Max buffered messages** — caps the queued messages; when the queue is full the oldest messages are discarded. The default is 1000.

## Sink configuration

### HTTP webhook (`http`)

- **Endpoint URL** (required) — the receiver URL (`http://` or `https://`).
- **HTTP method** — `GET`, `POST`, `PUT`, `PATCH` or `DELETE`; the body is the JSON envelope.
- **Username / password** — optional HTTP basic authentication.
- **Headers (JSON)** — optional headers added to every request, given as a JSON object.
- **Private CA certificate / client certificate / client key (PEM)** — trust an internal CA and/or present a client certificate for mutual TLS; system roots stay valid.
- **Skip certificate verification** — insecure switch; prefer a private CA certificate.
- Success is HTTP `2xx`/`3xx`; `408`, `429` and `5xx` are retried, other `4xx` are terminal.

### MQTT broker (`mqtt`)

- **Broker URL** (required) — `tcp://host:1883` for plain text, `ssl://host:8883` for TLS.
- **Topic** (required) — the topic each envelope is published to.
- **QoS** — 0, 1 or 2.
- **Client ID** — optional; generated when empty.
- **Username / password** — optional broker credentials.

### NATS server (`nats`)

- **Servers** (required) — external NATS server URL, for example `nats://host:4222`.
- **Subject** (required) — the subject each envelope is published to.
- **Username / password** — optional user credentials.
- **Token** — optional auth token (use instead of a username/password).

### RabbitMQ / AMQP (`amqp`)

- **URL** (required) — connection URL including vhost, for example `amqp://user:pass@host:5672/vhost`.
- **Exchange** — optional named exchange; empty publishes to the default exchange.
- **Routing key** (required) — the routing key each envelope carries.

### Apache Kafka (`kafka`)

- **Bootstrap servers** (required) — comma-separated `host:port` list.
- **Topic** (required) — the topic produced to.
- **Use SASL authentication** — enable to enter SASL/PLAIN credentials (`SASL_PLAINTEXT`, no TLS); leave off for an anonymous broker.
- **Username / password** — SASL/PLAIN credentials, shown and required when the SASL switch is on.

### Email via SMTP (`email`)

- **Host** (required) — SMTP server host.
- **Encryption** — `STARTTLS` (default) upgrades after EHLO and sets port **587**; `TLS/SSL` dials implicit TLS and sets port **465**; `None` stays plaintext and sets port **25**. The port auto-fills on switch; you can still override it.
- **Port** — server port, filled from the encryption mode (587/465/25).
- **From** (required) — sender address.
- **Recipients** (required) — comma-separated recipient addresses.
- **Subject** — optional subject line; the body is the JSON envelope.
- **EHLO domain** — FQDN announced in EHLO/HELO; defaults to the sender's domain (strict servers reject the client default `localhost`).
- **Use SMTP authentication** — enable to send username/password; **Username** is then required. With 587 the credentials are protected by the negotiated STARTTLS session.

### SMS via HTTP provider (`sms`)

- **Endpoint URL** (required) — the provider's HTTP send endpoint.
- **Recipients** (required) — comma-separated phone numbers.
- **Recipient JSON field / text JSON field** — provider field names carrying the number (default `to`) and message (default `text`); the worker POSTs them as form/JSON parameters.
- **Headers (JSON)** — optional headers (for example an API key) added to every request.
- Success follows the provider's HTTP status, using the same retry rules as the HTTP webhook.

## How a forwarder is triggered

A forwarder is a tenant-level resource; it does nothing until a rule chain references it. Add a **forward** node to a rule chain and set its `forwarder_id` to this forwarder. Messages reaching the node are queued for delivery. A publish failure is logged but never fails the rule chain.

## Retries and failures

- Network errors, timeouts, `408`, `429` and `5xx` responses are retried with backoff.
- Other `4xx` responses are permanent rejections.
- Broker-style sinks (MQTT, NATS, AMQP, Kafka) treat connection or publish errors (including rejected credentials) as retryable; SMTP and the HTTP SMS provider follow the same retry policy, bounded by the same max attempts.
- After the max attempts are exhausted, or on a permanent rejection, the message is parked in the **Dead Letters** tab.

## Dead letters tab

Inspect parked envelopes with the originating forwarder, device, attempt count and last error. Use **Replay** to re-publish one message onto its forwarder's queue. Replay is only possible while the forwarder still exists.

## Delivery Log tab

Each row is one real delivery attempt, showing the time, forwarder, device, attempt number and the receiver return code:

- **Delivered** — the receiver accepted the message (for example HTTP `200`).
- **Retrying** — a transient failure; the message will be retried.
- **Failed** — a permanent rejection; the message moved to dead letters.

Because delivery happens after the forward node returns, this code is not visible in the rule-chain trace; use this tab to see it. The log is bounded: rows older than 7 days and more than 1000 rows per forwarder are removed automatically.