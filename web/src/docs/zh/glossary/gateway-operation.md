<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 网关操作

**网关操作**是按需发起、作用于网关整机的即时动作，例如重启网关或触发厂商定义的维护例程。它与[连接器](help:glossary/connector)的长期声明式配置、与下发给受管[子设备](help:glossary/subdevice)的命令（ADR-0012）都不同。

- 平台没有内置任何操作词汇。每个操作都是一个**描述符**：标识名、交互方式（请求-响应/单向）、超时、参数/结果 JSON Schema 与风险级（`只读`/`普通`/`危险`）。
- 描述符来自网关 hello 广告、租户私建，或对广告描述符的私有克隆；租户可禁用任意操作。
- 调用“危险”操作必须通过一次全新的 TOTP step-up（操作时认证）挑战；参数可引用边缘密钥，在最后一跳还原。

详见 [网关操作](help:page/gateway-operations) 帮助页。
