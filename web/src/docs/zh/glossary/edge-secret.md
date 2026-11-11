<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 边缘密钥

**边缘密钥**是平台下发给[网关](help:glossary/gateway)保存和使用的凭据，例如第三方系统的用户名/密码。它与网关登录平台所用的设备凭据是两回事。

- 平台是正本：密钥集中加密存储，网关通过与[连接器](help:glossary/connector)相同的声明式通道接收。
- 网关只是副本：（重新）连接时整组密钥会重新投递，替换网关后密钥自动恢复。
- 平台不认识任何凭据协议。**密钥类型**描述符以 JSON Schema 声明一种密钥包含哪些字段，由网关广告或由运维人工定义；没有描述符时退回自由键值对表单。
- 下发的密钥可携带长期有效的目标选择器（如 MAC 地址），其含义完全由边缘侧解释。
- **密钥绑定**记录一把密钥在一个网关上的状态与单调递增版本：PENDING → DISPATCHED → APPLIED（或 FAILED + 原因），撤回为 REMOVING → REMOVED，在网关的“密钥”页管理。

详见 [边缘密钥类型](help:page/edge-secret-types) 与 [边缘密钥](help:page/edge-secrets) 帮助页。
