<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Rule chain

A **rule chain** routes device messages and performs tenant-defined actions. The platform itself carries no business logic of its own — business logic lives in rule chains.

Messages ([telemetry](help:glossary/telemetry), lifecycle, alarms, …) flow through linked **rule nodes** (filters, transforms, enrichments, actions) and are fanned out or stopped by each node's routing. A [device profile](help:glossary/profile) or an individual device may bind a rule chain, so all its traffic is processed by it.

The platform only supplies generic nodes and transport; tenants define every rule.
