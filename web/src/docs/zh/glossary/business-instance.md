<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 业务实例

**业务实例**是从[业务模板版本](help:glossary/business-template-version)创建的租户级部署草稿和生命周期资源。它保留完整模板快照，并拥有自己的参数值、具体资产绑定、装配和校验状态。

后续模板发布不会改写已有实例，编辑实例也不会发布模板版本。网关和摄像头选择器使用 NereidLink 的权威资源：摄像头从所选网关管理的设备中选择，切换或清空网关会同时清空摄像头。草稿可以保留占位，但部署前必须绑定装配要求的真实资产。

每次保存期望变更都会形成一个[业务代次](help:glossary/business-generation)及持久化的[业务展开](help:glossary/business-expansion)。部署会预检所有目标，监控读取真实健康状态。撤消部署只移除实例拥有的资源，不影响共享模型、pipeline 注册、伴生服务和转发器。

实例的创建、编辑、部署、监控、回滚、撤消部署和删除见[业务编排](help:page/business-orchestration)指南。
