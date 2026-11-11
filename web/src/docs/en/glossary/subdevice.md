<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Sub-device

A **sub-device** is a device that cannot connect to the platform on its own and is reached through a [gateway](help:glossary/gateway). The gateway owns and manages it.

- Its device **type** is "sub-device (gateway-managed)" and it has an owning gateway.
- [Telemetry](help:glossary/telemetry), [presence](help:glossary/presence) and commands flow via the gateway's northbound connection.
- It may have at most one bound [connector](help:glossary/connector); the connector column on the device list shows its reported state.

A **direct device** is the opposite case: it authenticates and connects to the platform directly.
