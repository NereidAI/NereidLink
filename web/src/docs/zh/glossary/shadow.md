<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 设备影子

**设备影子**为设备保存两份相互独立的文档：

- **期望（desired）**——平台希望设备达到的状态，由操作员和[规则链](help:glossary/rule-chain)写入。
- **上报（reported）**——设备最近一次自报的状态，仅由设备写入。

两者彼此独立，因此下发指令无需等待设备应答即可完成。逐键状态：期望等于上报时为**已同步**，设备尚未追上时为**待同步**。
