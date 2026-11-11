<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 业务编排

**业务编排**是 NereidLink 对现有摄像头、已注册 pipeline、模型、外部 servable、伴生服务、规则、动作和选择性北向输出进行的声明式装配。

- 画布只包含资产引用、标量参数以及订阅或触发关系，不接受脚本、表达式、用户代码、通用 DAG 执行或 NereidInference 逐帧 pipeline 拓扑。
- NereidLink 不执行摄像头帧。技术 pipeline 由 NereidInference 负责，领域结论和台账由伴生服务负责。
- 北向上传必须显式添加输出、选择已有转发器、声明字段白名单，并提供显式原始 EdgeX 设备／profile／资源选择器。embedding 保留在边缘本地总线，不能加入白名单；没有贡献者后，合并出的网关过滤器会被静默而不是删除。

每个[业务模板版本](help:glossary/business-template-version)都不可变。[业务实例](help:glossary/business-instance)把装配绑定到具体资源，并通过[业务代次](help:glossary/business-generation)和确定性的[业务展开](help:glossary/business-expansion)完成交付。

操作流程和当前集成限制见[业务编排](help:page/business-orchestration)指南。
