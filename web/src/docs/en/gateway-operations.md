<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Gateway Operations

The **Gateway Operations** page shows the aggregate operation catalog: descriptors each gateway advertises in hello, stored per gateway, plus tenant-defined private operations. The platform ships no operation names itself, and risk is never inferred from a name — it only comes from the descriptor. Two gateways advertising the same key keep independent copies; the page groups them into one row showing how many gateways advertise it (hover for the names).

## Descriptor fields

- **Operation** — the key the gateway understands.
- **Version** — optional descriptor version.
- **Interaction** — request/response (waits for a result) or one-way.
- **Risk** — read, normal or dangerous.
- **Timeout** — execution bound, 100–60000 ms.
- **English / Chinese name and description** — shown in the operator's UI language, falling back to English and then the operation key.
- **Parameters / result JSON Schemas** — the console renders parameter forms and result views from them at invocation time. The parameter schema can be built field-by-field (name, type, required flag, description) or edited directly with **Advanced JSON**; schemas using constructs the builder cannot represent stay in JSON mode.

## Managing the catalog

- Use the eye icon on any row to open its **read-only detail**: key, version, risk, interaction, timeout, source, bilingual names/descriptions and the full parameter/result JSON Schemas.
- Advertised operations (source: advertised) are read-only; from the detail view use **Clone to private** to edit a copy, or use the enable switch to disable them.
- Private operations can be viewed, edited and deleted.
- The enable switch disables an operation for this tenant; disabled keys stay visible so they can be re-enabled.

## Finding operations

- Use the search box (matches name, key, version and description) and the source/risk filters; the table pages locally.
- A red **Contract conflict** tag means the gateways advertising the same operation+version disagree on risk, timeout or a schema. Align the firmware builds, or have them advertise distinct keys; an invocation is always validated against the target gateway's own copy.
- A gateway's detail **Operations** tab is the authoritative list for that gateway and supports its own search, risk filter and pagination.

## Invoking from a gateway

On the gateway detail page, the **Operations** tab lists only operations this gateway's firmware advertised in its last hello (plus tenant-defined descriptors).

- Fill the schema-driven parameters. The platform re-validates them against the descriptor schema before dispatch, so a console that skips validation cannot send malformed params. A parameter value may reference a key from the vault with `secret://<name>`; the reference is resolved at the last mile and never stored as plaintext.
- **Read** actions execute immediately; **normal** actions require a real second confirmation (a confirmation popover after you press execute).
- You may shorten the wait with the **Timeout override** (minimum 100 ms); it can never raise the descriptor default, and values are clamped to 100–60000 ms.
- All operations are human-only: a machine (service-account) identity is refused at every risk level with `OPERATION_HUMAN_ONLY`. **Dangerous** actions additionally require a fresh authenticator (TOTP) step-up completed within the last five minutes. The modal shows a signed QR code describing the operation, gateway and expiry; verify it matches what you expect, then enter the current code. Refused dangerous attempts are recorded in the audit log even though no task is created.
- The result is shown in the modal, and every invocation (including the failure reason) appears in the operation history table below. Results are retained 90 days; the permanent who/when/which-gateway record lives in the chained audit log.
- An offline gateway fails immediately with **Not connected**; operations are never queued. Use device commands when you need delivery on reconnect.
