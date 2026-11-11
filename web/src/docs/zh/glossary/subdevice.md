<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 子设备

**子设备**是自身无法连接平台、需要经由[网关](help:glossary/gateway)访问的设备，由网关拥有和管理。

- 设备**类型**为"子设备（网关代管）"，并有所属网关。
- [遥测](help:glossary/telemetry)、[presence](help:glossary/presence) 和命令都经由网关的北向连接流转。
- 至多绑定一个[连接器](help:glossary/connector)；设备列表的连接器列显示其上报状态。

**直连设备**是相反的情况：自己完成认证并直连平台。
