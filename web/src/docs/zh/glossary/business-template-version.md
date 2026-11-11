<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 业务模板版本

**业务模板版本**是租户内由模板 ID 和版本共同标识的一份不可变[业务编排](help:glossary/business-orchestration)文档，定义装配、标量参数 JSON Schema 以及资产引用或占位。

- **导入模板包**上传 YAML 或 JSON 模板包（单个模板文档，或 `{"templates":[...]}` 封装）；**新增模板**从空白版本开始。二进制本身不携带任何业务词汇。
- 对任意版本点击**编辑**会固定模板 ID，并把修改发布为另一个不可变版本，绝不改写原版本。设计器会建议下一个补丁版本，也可以在保存前修改。
- 创建[业务实例](help:glossary/business-instance)时会快照完整选中版本。之后发布其他版本不会改变已有实例。

只有未被引用的版本可以删除。删除已引用版本返回 `409`；请求不存在的版本返回 `404`。删除实例不会删除其模板版本。

完整流程见[模板与实例](help:page/business-orchestration)。
