<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 探测（Probe）

**探测（Probe）**是对[子设备](help:glossary/subdevice)按需发起的一次性可达性检查：平台让其[网关](help:glossary/gateway)在现场总线上执行一次真实的读操作，并回报设备是否应答以及往返时延。平台只中继请求，不会把读数解释或存储为业务数据。

## Probe 与 Presence 的区别

[Presence（在线状态）](help:glossary/presence)是平台持续维护并缓存的连接层状态。对被动子设备而言，它只反映网关在自己的平台会话存活期间所声明的状态——设备在现场总线上掉线（断电、接线故障）时，在网关通过[存活探测（health check）](help:glossary/health-check)发现之前，仍可能显示在线。Probe 则在执行的那一刻证明端到端的应用层可达性，并返回实测往返时延；它不会更新 presence 或任何其他设备状态。

## Probe 与 Health Check 的区别

- **Probe（按需探测）**——手动发起的一次性批量检查，结果只展示给操作者。
- **Health Check（存活探测）**——配置在连接器实例上的周期探测循环，自动驱动绑定设备的在线/离线状态。

## Probe 模式

平台只校验并透传 `mode`，具体语义由边缘实现定义。

| 模式 | 推荐含义 |
|---|---|
| `cached` | 使用最近一次已知检查结果回答，不要求发起新的现场读。 |
| `fresh`（默认） | 立刻在现场总线上执行一次新的读。 |
| `strict` | 按最严格的检查规则执行一次新的读。 |

探测只能针对绑定到网关的子设备。网关离线时其设备不排队，立即以“未连接”失败。
