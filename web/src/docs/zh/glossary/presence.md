<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# Presence（在线状态）

**presence** 是平台对设备当前是否在线的实时视图：在线、离线或未知。

- **presence 边界**是一次在线/离线跳变事件（连接、断开、会话被驱逐）。
- **连接状态**由最近一条边界推导；*未知*表示设备从未产生过边界。
- **最后活动**取最新[遥测](help:glossary/telemetry)与最近 presence 边界中较新者，悬停连接徽标时展示。

presence 回答"现在连着没有"；[生命周期状态](help:glossary/lifecycle)回答"处于什么服役状态"（在用、隔离、停用）。两者相互独立。

对被动子设备而言，presence 不证明现场总线可达：设备本身掉线后它仍可能显示在线。端到端的确认需要按需[探测（probe）](help:glossary/probe)。
