<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 业务展开

**业务展开**是针对一个[业务代次](help:glossary/business-generation)确定性生成的 `nereidlink.business-expansion/v1` 文档。它把业务装配转换为封闭的声明式产物：模型分发请求、pipeline 与外部 servable 注册、推理连接器绑定、伴生服务订阅、规则链、显式北向通道，以及每个受影响网关的一个网关级北向过滤单例。

产物依赖只描述交付顺序，不是运行时逐帧处理图。相同输入会产生逐字节一致的产物和相同的 SHA-256 摘要；稳定的产物键及保留的资源映射使重试和回滚保持确定性。

**预览展开**用于检查确切的已保存代次，不代表产物已经部署。部署仍须通过完整校验和目标预检，外部契约或能力缺失仍是阻断问题。产物由原资源所有者应用；NereidLink 不会新增第二套连接器传输或通用 DAG 执行器。

所有权与交付边界见[业务编排](help:page/business-orchestration)和[网关集成](help:page/integrate-gateway)指南。
