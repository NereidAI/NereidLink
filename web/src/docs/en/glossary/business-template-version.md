<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Business template version

A **business template version** is one immutable [business orchestration](help:glossary/business-orchestration) document identified by a template ID and version within a tenant. It defines the assembly, scalar parameter JSON Schema, and asset references or placeholders.

- **Import pack** uploads a YAML or JSON template pack (a bare template document or a `{"templates":[...]}` envelope); **Create template** starts a blank version. The binary itself ships no business vocabulary.
- **Edit** on a version keeps its template ID fixed and publishes the changes as another immutable version; it never rewrites the source version. The designer suggests the next patch version, which you can change before saving.
- Creating a [business instance](help:glossary/business-instance) snapshots the complete selected version. Publishing another version does not alter existing instances.

Only an unreferenced version can be deleted. Deleting a referenced version returns `409`; requesting a missing version returns `404`. Deleting an instance does not delete its template version.

See [Templates and instances](help:page/business-orchestration) for the complete workflow.
