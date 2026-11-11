<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Storage Hooks

Storage hooks run before [telemetry](help:glossary/telemetry) is written to the database. They implement validation, enrichment and forwarding.

## Hook types

- **expression** — a JavaScript script that mutates the incoming point.
- **webhook** — the point is sent to an HTTP URL.
- **wasm** — a WebAssembly module (WASI) executed in-process in a sandbox.

## Common fields

- **Order** — hooks run in ascending order.
- **Measurement keys** — comma-separated keys the hook applies to; empty means every key.
- **Timeout (ms)** — maximum execution time.
- **Failure policy** — what happens when the hook fails:
  - **passthrough** — store the original point anyway.
  - **abort** — do not store the point.
  - **dlq** — move the point to the Dead Letter Queue.

## Expression example

```javascript
point.v_double = point.v_double * 2
```

## WASM hooks

A **wasm** hook runs a sandboxed WebAssembly module (WASI) with no network or
filesystem access. The incoming point is passed to the module's **stdin** as
one JSON object; the module prints one JSON object to **stdout** that overlays
the point using the same contract as an expression hook:

- `{"v_double": 42, "unit": "C"}` overlays fields;
- `{"drop": true}` aborts the point;
- empty stdout leaves the point unchanged.

Hard budgets: 16 MiB memory, 64 KiB output and the configured per-hook timeout
(an infinite loop is killed). Upload a `.wasm` file or paste standard base64
(`config.module_b64`) in the hook editor; modules default to a 2 MiB cap
(operator-configurable). When editing, leave the module field empty to keep the
stored binary. Modules are validated (compiled) on save, so an invalid binary
is rejected immediately.

Use the Dead Letter Queue page to inspect and replay points rejected under the `dlq` policy.
