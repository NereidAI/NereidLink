<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Notifications

The Notifications page is the in-app notification inbox.

## States

- **Unread** — new notifications.
- **Read** — notifications already viewed.
- **All** — everything.

## Actions

- **Mark as read** — marks a single notification read.
- **Mark all read** — marks every unread notification read at once.
- **View device** — jumps to the originating device when the notification is device-related.

Notifications are produced by alarm events and system services, and also arrive in real time over the WebSocket. External delivery (email, webhook) is configured separately.
