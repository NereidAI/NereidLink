<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Device Shadow

The **[Device shadow](help:glossary/shadow)** tab of a device shows its *desired* and *reported* state side by side, and lets you write desired state.

## The model

Device shadow separates **what you want** from **what the device says it has**:

- **Desired** — state the platform wants the device to reach. The platform and [rule chains](help:glossary/rule-chain) write it; an operator edits it from this tab.
- **Reported** — state the device last published about itself. Only the device writes this.

The two are separate documents, so a command can be issued without waiting for the device to answer.

## Reading the table

The table lists the union of keys from both sides. For each key it shows the desired value, the reported value, and a status tag:

- **Synced** (green) — the desired and reported values for that key are identical.
- **Pending** (orange) — they differ; the device has not yet caught up.

A summary banner shows the overall shadow state (in sync or out of sync) together with the `desired` and `reported` version numbers. The version increments on every write and is how concurrent edits are detected.

## Writing desired state

1. Click the edit action.
2. **Common keys** — when the device's [profile](help:glossary/profile) declares writable points (`read_write` W/RW), pick one from the top selector. It adds a fixed-key row with a typed control: a switch for booleans, a bounded number input (the profile's engineering range and unit are applied) or a text input. Remove a row to delete that key.
3. **Custom keys** — add a free key/value row when you need a key the profile does not declare, choosing boolean, number, string, object or array.
4. **Additional keys (JSON)** — for composite or bulk values, paste an extra JSON object; its keys are merged with the rows above.
5. Save. The result is **merged** into the existing desired document — keys you neither edit nor remove are left untouched; removing a previously present key sends a JSON `null` that deletes it.
6. The write uses optimistic concurrency: the version you loaded is sent as an `If-Match` ETag. If someone else changed the shadow in the meantime, the write is rejected instead of silently overwriting their change; reload and retry.

A device whose profile declares no writable points offers only the custom rows and the JSON box. Desired state is a target, not a command. Reaching it is the device's job — usually via a rule chain that watches the shadow and issues the actual command.

## Notes

- Values are arbitrary JSON; the platform does not interpret them.
- Writing desired state does not itself change device state or [connection state](help:glossary/presence).
