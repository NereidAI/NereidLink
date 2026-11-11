<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Operations Orders (Platform)

The **Operations Orders** page is the super administrator's confirmation queue for destructive operations. Only the single super administrator can see and act on this queue.

## Why it is a queue and not a form

Destructive operations (bulk deletion, quarantine release, data retention changes) cannot be started from the web. Instead:

1. A **platform operator** opens an order from the CLI, which records the operator name, the exact action, and a payload.
2. The order lands here as `PENDING`.
3. The **super administrator** reviews the full payload and content hash, then confirms or refuses it.

This separation of duties means no single person can both request and authorize a destructive action.

## Order lifecycle

- `PENDING` — awaiting the super administrator's decision
- `APPROVED` — confirmed; the action ran and the result was recorded
- `EXECUTED` — same as approved, kept as the final executed state
- `REJECTED` — refused by the super administrator, with a reason
- `EXPIRED` — not confirmed before its deadline; it can no longer be run
- `CANCELLED` — withdrawn before confirmation

## Reviewing an order

- **View** — open the order to see the action, the full JSON payload, the initiator, the content hash, and — for finished orders — the execution result.
- **Approve** — confirm and run the action immediately. A decision reason is recorded.
- **Reject** — refuse the order. A reason is required so the operator knows why.

## Notes

- The content hash shown in the review view lets you confirm the payload has not changed since it was opened.
- Expired and cancelled orders stay in the list for the audit trail, but can no longer be approved.
