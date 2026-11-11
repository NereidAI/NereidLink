<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Rule Chains

Rule chains route device messages and execute tenant-defined actions. The platform itself carries no business logic — this is where your logic lives.

## Chain groups

A "rule chain" is a **chain group** holding multiple immutable versions. [Device profiles](help:glossary/profile) and devices bind to the group, so activating a new version never changes a binding.

Each tenant has exactly one **default group**, used as the fallback when nothing is explicitly bound.

## Bindings and resolution order

For every message the executed chain is resolved by priority:

1. **Device binding** — a chain set on one device fully replaces its profile chain.
2. **Profile binding** — devices sharing a profile share one chain.
3. **Tenant default group** — used when neither level is bound.

Bindings are configured in the device form (device level) and the profile form (profile level).

## Versions

Every chain is immutable per version:

- **DRAFT** — a saved, editable candidate.
- **ACTIVE** — the single version currently processing messages.
- **ARCHIVED** — superseded or retired versions.

Editing never modifies a version; saving creates a new draft. Activating a version atomically archives the previous active version of the same group.

## Working with chains

- Click **New chain** to create a group; the editor opens automatically to build the graph.
- When the latest version of a group is a draft, use **Activate** to make it live. The active version is marked in the list.

## Deleting chains

A chain group can be deleted only when it is **neither the tenant default nor bound to any device or profile**. The delete button is disabled otherwise; hover it to see the current device/profile binding counts. Before deleting, unbind the referencing devices or profiles (with no explicit binding they fall back through the resolution order). Deleting permanently removes every version of the group.

See the Rule Chain Editor help for how to build graphs.
