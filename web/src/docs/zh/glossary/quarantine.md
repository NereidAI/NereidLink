<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 隔离

**隔离**是对单台设备的可逆事件响应锁。当设备疑似凭据泄露、被盗、洪泛或行为异常，需要立即让它停下又不丢失历史时使用。

隔离把设备置为 `QUARANTINED` [生命周期状态](help:glossary/lifecycle)，驱逐其 [presence](help:glossary/presence) 与在线会话，并使后续认证失败——设备无法重连、无法上报[遥测](help:glossary/telemetry)、无法接收命令，而凭据仍保留在册。身份、归属、遥测与历史全部保留，解除隔离即可恢复服役，无需重新发放凭据。

隔离是生命周期动作，与[连接状态](help:glossary/presence)相互独立。原因码与解除流程见[隔离](help:page/quarantine)指南。
