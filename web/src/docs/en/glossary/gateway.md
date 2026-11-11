<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Gateway

A **gateway** is a device that mediates between the platform and other, non-directly-connected devices. It holds a southbound connection to its managed devices and a single northbound link to NereidLink.

- A gateway owns its **managed [sub-devices](help:glossary/subdevice)** and runs the [connectors](help:glossary/connector) that talk to them.
- It appears in the device list like any device and has a **Connectors** and a **Managed devices** tab on its detail page.
- The platform is protocol agnostic about what runs on a gateway; any edge implementation speaking the wire contract may register devices and connectors.

Contrast with a **direct device**, which connects to the platform itself.
