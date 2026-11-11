<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 命令中心

命令中心追踪租户内每台设备的命令。

## 过滤

- 可按 **命令名称** 或部分 **设备名称/别名** 搜索。
- 状态过滤：可选择单个生命周期状态，或选择 **进行中** 匹配所有非终态（`PENDING`、`CLAIMED`、`QUEUED`）。

## 生命周期状态

- `PENDING` / `QUEUED` — 等待投递。
- `CLAIMED` — 某个接入服务已承担投递责任。
- `DELIVERED` — 已发送给已连接的设备。
- `SUCCEEDED` — 设备返回成功响应。
- `FAILED` / `TIMEOUT` / `NOT_CONNECTED` / `EXPIRED` — 终态失败原因。

## 列

每行显示创建时间（按当前控制台语言格式化）、设备 Canonical name（下方显示别名，链接到设备详情）、命令名称、交互方式（one-way 或 request-response）、时效方式（realtime 或 persistent）及当前状态。

## 批量命令表单

- **目标**——设备组（从组列表选择）或显式设备；显式设备为可搜索的设备名下拉框，无需手填 UUID。
- **超时时间**——预填 30 秒作为参考值，可按需修改。
