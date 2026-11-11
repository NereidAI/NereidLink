<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Business orchestration

**Business orchestration** is NereidLink's declarative assembly of existing cameras, registered pipelines, models, external servables, companion services, rules, actions, and selective northbound outputs.

- The canvas contains asset references, scalar parameters, and subscription or trigger relationships. It accepts no scripts, expressions, user code, general DAG execution, or NereidInference per-frame pipeline topology.
- NereidLink does not execute camera frames. NereidInference owns the technical pipeline, while companion services own domain conclusions and ledgers.
- Northbound upload requires an explicit output, an existing forwarder, a field whitelist, and explicit raw EdgeX device/profile/resource selectors. Embeddings remain on the edge-local bus and cannot be whitelisted; the merged gateway filter is quiesced rather than deleted when no contributor remains.

Each [business template version](help:glossary/business-template-version) is immutable. A [business instance](help:glossary/business-instance) binds that assembly to concrete resources and is delivered through [business generations](help:glossary/business-generation) and deterministic [business expansions](help:glossary/business-expansion).

See the [Business orchestration](help:page/business-orchestration) guide for the operator workflow and current integration gates.
