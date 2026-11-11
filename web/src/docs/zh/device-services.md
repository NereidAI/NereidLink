<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 设备服务

**设备服务**页是一个可见且可编辑的目录，收录 NereidLink 能够渲染结构化表单的协议族与参考 [EdgeX](https://docs.edgexfoundry.org/4.0.2/microservices/device/DeviceService/) 设备服务。

## 目录是什么

每个目录条目（一个**协议族**）记录：

- **协议族键**——[设备配置](help:glossary/profile)上存储的开放词汇协议族，如 `s7`、`modbus`。
- **显示名称**与**服务名**——参考 EdgeX 设备服务（如 `device-s7`），用于为空白[连接器](help:glossary/connector)模板的 `ds_name` 赋初值。
- **协议键 / 传输**——EdgeX 设备 `protocols` 映射下可能出现的具体键（如 `s7`、`modbus-tcp`）。
- **连接参数**——在配置/设备连接器编辑器中渲染的结构化字段，并按原样写入协议属性映射。
- **点位地址参数**——每个测点描述符上渲染的结构化字段，并写入点位地址对象。
- **默认点位地址**——新建测点时预填的初始地址对象（如 `{"NodeName": "DB4.DBX0.0"}`）。留空则按各参数自身的默认值推导。
- 官方文档链接。

该目录是**全局**的：相同的厂商事实适用于所有租户。平台内核本身不解析任何协议；目录仅用于驱动界面表单与提示。接入与解析保持与传输无关。

## 内置与自定义

- EdgeX 4.0.2 设备服务索引中列出的十四项设备服务作为**官方**条目预置：Modbus、ONVIF 摄像头、MQTT、REST、device-virtual、SNMP、BACnet、CoAP、GPIO、OPC-UA、RFID LLRP、S7、UART 与 USB 摄像头。
- 官方条目**可以编辑**（修正标签、增加字段），但**不能删除**。
- 使用**新增设备服务**定义自定义协议族；自定义条目可以编辑与删除。

## 与设备配置的关系

选择配置的**协议族**后，会启用该族的结构化连接参数（在连接器模板与网关连接器实例上）以及点位地址字段（在每个描述符上）。没有固定属性的协议族或不在目录中的协议族会回退到自由 JSON 编辑；任何未知内容都不会被拒绝。

## S7 说明

- `device-s7` 使用 ISO-on-TCP。连接属性为 `Host`、`Port`（102）、`Rack`、`Slot`，以及**整数秒**形式的 `Timeout`/`IdleTimeout`（不是时长字符串）。
- 点位地址仅使用单个属性 `NodeName`，如 `DB4.DBX0.0`、`DB4.DBD4`。
- 粘贴的 EdgeX 设备配置中，若资源带有 `NodeName`，会自动标记为 `s7` 协议族。网关上报的最小帧若不含属性，则在边缘侧补充之前无法自动标记。
