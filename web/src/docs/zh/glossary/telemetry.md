<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 遥测

**遥测**是设备上报的时序数据——传感器读数以及其他数值或离散采样。

- **测点**（测点键）是一个有名字的信号；其含义、单位和转换方式来自绑定的[设备配置](help:glossary/profile)描述符。平台从不发明单位。
- 采样按设备、测点键和时间寻址，驱动仪表盘、曲线和规则。

遥测是观测数据；[设备影子](help:glossary/shadow)保存当前/期望状态，属性保存相对静态的元数据。
