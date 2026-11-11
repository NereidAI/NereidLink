<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Audit Chain (Platform)

The **Audit** page shows the tamper-evident audit log. It is only available to platform administrators.

## Overview

Audit events record security-relevant actions across the platform. Each row is part of a **hash chain**: every event's hash is computed from the previous event's hash plus the event's canonical content. Because each row commits to the one before it, altering or removing any row breaks every hash after it.

Each event carries a sequence number, timestamp, actor, event type, action, target resource, source IP, and a per-row `prev_hash`/`hash` pair.

## Filtering

Use the filters to narrow the log:
- **Event type** — the category of the recorded action
- **Tenant** — limit to one tenant's events
- **Actor** — limit to events performed by a specific user

## Verify

Click **Verify** to run a full integrity check on the chain. The verifier:
1. Reads every event in sequence order and checks the sequence numbers have no gaps.
2. Recomputes each row's hash from the previous hash and the row content, comparing against the stored hash.
3. Checks the daily hash seals.

The result reports the number of events checked, whether the chain is intact, and — if not — the sequence number where the break was detected and why.

## Why it matters

The audit chain gives evidence that log history has not been rewritten. A successful verify means the log is internally consistent; a failure pinpoints the first altered row. Because each row commits to its predecessor, an attacker cannot silently edit a past event without detection.
