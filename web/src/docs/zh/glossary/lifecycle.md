<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 生命周期状态

设备的**生命周期状态**是其服役状态，与实时连接状态相对。

- `ACTIVE`（在用）—— 服役中；设备列表默认只显示在用设备。
- `QUARANTINED`（隔离）—— 被[隔离](help:glossary/quarantine)可逆锁定。
- 停用/归档状态是**墓碑**：设备永不物理删除，历史与身份始终可审计。

生命周期与 [presence](help:glossary/presence) 相互独立：在用设备可能离线，连通性也不说明设备是否在服役。
