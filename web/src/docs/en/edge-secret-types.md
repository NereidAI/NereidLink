<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Edge Secret Types

The **Edge Secret Types** tab (inside the **Edge Secrets** page) manages the descriptors for the credentials the platform delivers to gateways ([edge secrets](help:glossary/edge-secret)). A descriptor only declares which fields a credential carries — the platform itself recognizes no credential protocol.

## What a descriptor contains

- **Type key** — the credential type identifier understood by the gateway.
- **Version** — optional descriptor version; empty means unversioned.
- **English / Chinese name** — bilingual display names.
- **Fields schema** — a draft 2020-12 JSON Schema for the credential document. The console renders credential forms from it.

## Two sources

- **Edge** — the gateway advertises descriptors in hello; they are registered per tenant and marked read-only. Re-registering on every connection keeps the catalog current.
- **Manual** — tenant-defined descriptors, full CRUD. Use these for gateways that do not advertise, or click **Clone to manual** on an edge descriptor to edit a copy.

## Authoring modes

- **Field builder** — add rows of: field key, control type (text, password, integer, number, switch, dropdown), bilingual labels, required flag and, for dropdowns, a comma-separated option list. The builder generates the JSON Schema. Editing a manual descriptor loads its saved fields back into the builder.
- **JSON Schema** — paste or edit a complete schema directly.

The eye icon **opens** a descriptor read-only: a field overview (key, kind, required, label). For an edge descriptor use **Clone to manual** there to create an editable copy; for a manual descriptor click **Edit** in the overview to switch straight to editing. The pencil icon (manual rows only) opens it directly in edit mode.

Unregistered types are always accepted with an ad-hoc key/value form.
