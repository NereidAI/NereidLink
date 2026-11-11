<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Escalation Chains

The Escalation Chains page defines who is notified when an alarm is not handled in time.

## Chain structure

A chain contains an ordered list of steps. Each step defines:

- **Delay (s)** — seconds from the alarm start after which the step fires.
- **Notify role** — the tenant role whose members are notified (`TENANT_ADMIN`, `ENGINEER`, `CUSTOMER_USER`, `READ_ONLY`).
- **Notify group** — optional device group narrowing the step to role holders whose data scope covers that group's subtree. Leave empty to notify every holder of the role in the tenant.
- **Stop on ack** / **Stop on clear** — cancel the step if the alarm is acknowledged or cleared before the delay elapses.

## How escalation fires

A background process checks every 10 seconds. Each due step fires exactly once: it posts the configured webhook, emails the members of the target role, and records an in-app notification. When a step names a device group, only role holders whose data scope covers that group's subtree are emailed. Editing a chain does not affect steps that have already fired.

## Binding a chain

Open the rule chain editor, select a **Create Alarm** node, and choose the escalation chain in its configuration. Alarms raised by that node then follow the selected chain.
