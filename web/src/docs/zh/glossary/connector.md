<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 连接器

**连接器**是运行在[网关](help:glossary/gateway)上、由平台管理的协议连接器*实例*，是某类连接器在一台网关上的具体落地。

- 连接器要么是**网关级**（不绑定设备），要么恰好绑定一个代管[子设备](help:glossary/subdevice)。
- 平台是事实记录系统：你声明**期望规约** `{connector_type, type_version, config, mappings}`，由网关上报实际应用结果（`CREATED`、`DISPATCHED`、`APPLIED`、`DEGRADED`、`FAILED`）。
- 连接器的 `config` 和 `mappings` 只由边缘侧解释；平台不理解协议细节。

绑定的[设备配置](help:glossary/profile)提供可复用的测点表；下发时实例覆盖项合并到模板之上。[健康检查](help:glossary/health-check)（`health_check`）仅在绑定设备的连接器上可用。

完整操作见[连接器](help:page/connectors)页面指南。
