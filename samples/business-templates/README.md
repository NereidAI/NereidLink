<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Business template packs

These reference packs are the importable business vocabulary of NereidLink.
The compiled binary ships **no** business templates of its own: a fresh
deployment has an empty template gallery until an operator imports a pack or
publishes a version through the designer.

## Contents

| Pack | Template ID | Version | Purpose |
|---|---|---|---|
| `face-access-control-0.0.1.yaml` | `face-access-control` | `0.0.1` | Camera, face detector/embedding models, face-service companion subscription, declarative access rules, door command and unknown-person alarm |
| `meter-reading-1.0.0.yaml` | `meter-reading` | `1.0.0` | Meter camera pipeline, model requirement, meter-service companion, retry/alarms and selective northbound egress |

Both documents contain only asset references, symbolic requirement refs,
instance parameter JSON Schema and a closed-vocabulary business relationship
graph. They never contain scripts, expressions, executable code or per-frame
NI pipeline topology; the business module rejects such content on import.

## Optional action nodes

`send-command` and `create-alarm` are optional, domain-free CEP primitives. A
template may use no action nodes and end with selective northbound egress. The
default delivery form is scalar conclusion egress; NL emits generic requests,
while physical action semantics and execution belong to external systems.

## Pack format

A pack is YAML or JSON. A file may contain either one bare template document
or an envelope holding several versions:

```yaml
templates:
  - schema_version: nereidlink.business-template/v1
    id: face-access-control
    version: 0.0.1
    # ...
```

Every template must carry `schema_version: nereidlink.business-template/v1`.
Import is all-or-nothing: an invalid document, an in-pack duplicate, or an
already-existing `id@version` fails the whole request before any row is
written.

## Model node assets

A `model` assembly node requires `model_id` and `version`; both are data,
never kernel constants. The model id must match the name NereidForge registers
for the package (or an approved edge-inventory alias), and `version` must match
the Forge package version — alignment is pack/assembly data, not a package
internal edit. Three optional assets control the NereidHorizon ensure envelope
(`nh-model-distribution/v1`); omitting them keeps the historical expansion
digest unchanged:

| Asset | Default when omitted | Meaning |
|---|---|---|
| `precision` | `fp16` | Closed hardware-aware selector: `fp16` or `int8`. One IR per precision serves every edge device. |
| `object_type` | `inference-model` | ESS object type token; safe characters only (`[\w.-~!@#$^*]`). |
| `object_id` | `<model_id>-<version>-<precision>`, sanitized | Deterministic ESS object id; the ensure idempotency key together with org and object type. |

The ESS destination for a gateway is not pack data: the deployment maps each
NL gateway id to an ESS destination token in the `horizon.gateway_destinations`
config section. An unmapped gateway blocks preflight rather than guessing a
target.

## Importing and exporting

REST (tenant-scoped, `business:write` for import and `business:read` for
export):

```bash
curl -X POST "https://<host>/v1/tenants/<tenant>/business-templates/import" \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/yaml" \
  --data-binary @face-access-control-0.0.1.yaml

curl "https://<host>/v1/tenants/<tenant>/business-templates/export?format=yaml" \
  -H "Authorization: Bearer <token>" -o business-templates.yaml
```

CLI (uses the merged server configuration for the database DSN and the seed
tenant):

```bash
nereidlink templates import -f face-access-control-0.0.1.yaml
nereidlink templates export -o business-templates.yaml
```

Imported versions are immutable. Editing in the console always publishes a
new version with the same template ID, and a version can only be deleted while
no business instance references it.
