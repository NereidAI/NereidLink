<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Health check

A **health check** (`health_check`) is an optional probe spec on a device-bound [connector](help:glossary/connector). The gateway periodically reads a resource and **discards the reading** — a probe never produces telemetry or [device shadow](help:glossary/shadow) data.

- Fields: `resource`, `interval_ms` (default 30000), `timeout_ms` (default 5000, must be shorter than the interval), and `fail_threshold` (default 3).
- The gateway owns the online/offline edge: it marks reachability from consecutive probe results.

Health checks are only meaningful when the connector is bound to a [sub-device](help:glossary/subdevice); a gateway-level connector has no single resource owner to probe.
