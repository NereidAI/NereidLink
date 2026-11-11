<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 业务代次

**业务代次**是[业务实例](help:glossary/business-instance)的一次带编号的期望修订。它不是[业务模板版本](help:glossary/business-template-version)：一个不可变模板版本可以创建多个实例，每个实例都有独立的代次历史。

- `generation` 标识最近保存的期望装配和参数。更新时发送 `expected_generation`；过期编辑器会收到冲突，而不会覆盖其他操作员的修改。
- `pending_generation` 标识正在应用的部署或回滚目标，可能与最新期望代次不同。
- `active_generation` 标识已通过健康回读确认的代次。失败或仍在等待的发布不会取代上一个生效代次。

每个代次都保留确定性的[业务展开](help:glossary/business-expansion)及已应用资源映射。回滚通过相同的预检和资源所有者路径重新应用保留的代次。
