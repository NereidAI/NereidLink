<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 连接器

「连接器」页签仅出现在**[网关](help:glossary/gateway)设备**上。它以声明式 desired/reported 模型（ADR-0026）管理运行在该网关上的协议[连接器](help:glossary/connector)实例。

## 概念

- NereidLink 是记录系统：你声明 **desired spec（期望规格）**，边缘网关回报实际已应用的状态。
- 期望规格为 `{connector_type, type_version, config, mappings}`。
  - **connector_type** — 连接器插件类型，例如 `modbus-tcp`。新的连接器类型由边缘注册，内核不理解协议细节。
  - **type_version** — 插件版本；留空则使用已注册的最高版本。
  - **config** — 一个 JSON 对象，按边缘为该连接器类型注册的 schema 校验。
  - **mappings** — 对象数组；每个对象必须含非空字符串 `key`，命名该连接器映射到的[遥测](help:glossary/telemetry)测点。编辑器会从绑定 profile 的[描述符](help:glossary/profile) measurement key 给出该 `key`（以及每条 EdgeX 自动事件的数据源名）候选，但仍可自由输入。其他属性因连接器而异（寄存器地址、数据源名称等）。
- **两级配置**：[设备配置](help:glossary/profile)（profile）存放可复用的**模板/点表**，连接器实例存放按设备区分的连接参数。下发时以实例覆盖项合并到配置模板之上。因此不绑定配置也可直接填写 `config` 与 `mappings` 来创建连接器。
- 连接器可以是**网关级**（不绑定设备），也可绑定到该网关托管的某个[子设备](help:glossary/subdevice)。

### 内置类型：`nereid-edge`

即使还没有任何网关注册 schema，类型选择框里也始终提供 `nereid-edge`。它标识内置的 NereidEdge 运行时（EdgeX App Service），其行为已写死，因此它的配置和映射通常保持默认值即可——`{}` 和 `[]`。其他类型（如 `modbus-tcp`、`onvif`、`mqtt-remote`）只在已连接网关注册后出现，但你仍可手动输入任意自定义值。

对于 EdgeX 家族连接器类型（`nereid-edge` 以及 `edge-connector`/`edgex-*` 别名），结构化编辑器按 NereidEdge 下发给 core-metadata 的 EdgeX v4 设备契约渲染标准字段：设备服务名（`ds_name`）、EdgeX profile 名、设备名、描述、标签（labels）、协议（protocols）与自动事件（auto events）；`tags`、`properties` 等高级字段仍在 JSON 模式编辑。在网关详情中，**EdgeX profile 名**是可搜索的“选择或输入”框，候选来自该网关注册上报的 profile（`v1/gateway/profiles`）；选定 profile 后，每条自动事件的**数据源名（source name）**会从该 profile 的设备资源中给出候选，也始终允许手动输入未上报的值。**协议**按 EdgeX 协议名一卡片一条。当绑定的配置（或所选配置模板）声明了已知协议族时，卡片提供结构化连接参数和传输方式选择（Modbus 为 `modbus-tcp`/`modbus-rtu`/`modbus-ascii`，通用字段 `Address`/`Port`/`UnitID`，RTU/ASCII 增补串口 `BaudRate`/`DataBits`/`StopBits`/`Parity`，以及超时时长字段）；同一字典还提供 ONVIF（`Onvif`：`Address`/`Port`/`MACAddress`/`FriendlyName`/`EndpointRefAddress`）、MQTT（`mqtt`：`CommandTopic`）、REST（`REST`：`Host`/`Port`/`Path`/`urlRawQuery`）、device-virtual（`other`：`Address`/`Port`）、SNMP（`TCP`：`Address`/`Port`）、BACnet（`BACnet-IP`/`BACnet-MSTP`：`DeviceInstance`/`Port`/`MAC`）、CoAP（`COAP`：`ED_ADDR`/`ED_SecurityMode`/`ED_PskKey`）；额外键仍可按类型化键值对编辑。未知协议族时可选择 `modbus-tcp`/`opcua`/`mqtt` 等或输入自定义键，每个属性值按字符串、数字或布尔分类输入。**自动事件**覆盖完整 EdgeX `AutoEvent` 结构：间隔（如 `10s`）、数据源名、`onChange` 开关（打开后显示阈值），以及高级保留策略（`maxCap`、`minCap`、时长）。当实例绑定单个子设备且 EdgeX **设备名**留空时，控制台会在保存时用该子设备在 NL 中的名称（`canonical_name`）补全。在设备作用域下对多个子设备批量创建（多选或全选）时，留空的设备名会**逐设备**补全：每个新建连接器各自使用对应子设备的 NL 名称，保证边缘设备正确命名。名称会被清洗为 EdgeX 合法字符（字母数字、`_`、`-`），例如自动 provision 名 `<gateway>/<local_id>` 中的 `/` 会变成 `-`。手动填写设备名始终覆盖该默认值。

## 如何编写 config 和 mappings

每个 JSON 字段默认打开为**左右分栏编辑器**：左侧是结构化输入，右侧是只读的实时 JSON 文档。**JSON** 模式仍是结构化字段未覆盖键的逃生通道；切回结构化模式要求文本可解析并满足入口契约（对象/数组形态，以及有边缘注册 schema 时的校验）。

绑定了**配置模板**的连接器实例，结构化侧只编辑本实例的覆盖项，右侧预览**生效文档**——模板与覆盖深合并后的结果，与实际下发完全一致（对象按键深合，数组和标量整体替换；实例 mappings 非空时整体替换模板数组）。

`config` 是一个 JSON **对象**；`mappings` 是一个 JSON **数组**，其元素为每个都带非空字符串 `key` 的对象。具体其他键名取决于连接器类型，由边缘按其注册的 schema 校验——平台不解析它们。

内置 `nereid-edge`（config/mappings 保持默认即可）：

```json
{ }
```

```json
[ ]
```

典型的 Modbus TCP 连接器：

```json
{ "host": "192.0.2.10", "port": 502, "unit_id": 1, "byte_order": "BIG" }
```

```json
[
  { "register": "40001", "key": "voltage" },
  { "register": "40002", "key": "current" }
]
```

两级配置时，把公共部分（总线设置、点表）放进**配置模板**，实例的 config 只写每台设备的差异（这台 PLC 的 `host`/`port`）；对象会深合并，而实例的非空 mappings 数组会整体替换模板数组。

## 生命周期状态

`PENDING → DISPATCHED → CREATING → APPLIED`，异常结果为 `FAILED` 或 `DEGRADED`。期望规格不会丢失：边缘会在重连后自动重试，且下发幂等。显式删除会先提交带 revision 的持久 tombstone 再尝试下发，并在每次 hello 后及后台周期以有界且公平的批次重放；传输成功、超时、普通 apply ACK 和 manifest 缺项都不构成删除确认。悬停状态标签可查看回报的原因。

只有当前 desired revision 的状态报告才会被接受。可选任务统计分别保留成功／错误计数和 P50/P95 延迟的可用性：零是真实读数，缺字段表示不可用。manager 资源还可回报观察到的 manager revision 和完整回读 hash；它们与 manifest 对账 hash 相互独立。

## 对账

网关周期性发送 manifest，列出其实际运行的连接器实例。该页签将 **reported（实际）** 集合与期望集合比较，并对每个规格分类：

- **In sync（一致）** — 期望与实际匹配。
- **Missing（缺失）** — 已期望但网关上不存在。
- **Stale（过期）** — 回报版本落后于期望版本。
- **Drifted（漂移）** — 存在，但其回报配置与期望不同。
- **Extra（额外）** — 网关上运行但未在 NereidLink 声明。

默认情况下漂移只告警；在 **strict reconcile（强制对账）** 模式下会用期望规格覆盖边缘中缺失、过期或漂移的已声明实例。即使在 strict 模式下，**Extra** 也只观察、不自动删除：无类型的 manifest 身份无法安全授权删除旧连接器、manager 或北向过滤资源。在网关发送首个 manifest 之前没有水位线，分类显示为未知，并展示信息提示横幅。该页签没有刷新按钮：它通过 WebSocket 的 `connector` 流保持实时，当网关回报期望/状态/manifest 变化时会自动重新获取列表与对账视图。

**设备 Profile / 协议**列显示连接器所用的设备配置（优先取绑定的配置模板，否则回退为绑定子设备的配置），并链接到其详情页；已知协议时在下方显示协议族；无法解析时显示 `-`。

**绑定子设备**列链接到绑定的设备，名称下方显示其别名；网关级实例则显示网关级标签。

## 筛选与搜索

表格上方的工具栏靠右排列，在本网关的连接器集合上本地过滤：

- **连接器类型**、**上报状态**、**对账状态**和**目标**（网关级 / 绑定子设备）下拉。
- 搜索框按连接器类型、类型版本、Profile 名/协议与绑定子设备名称匹配（不区分大小写、支持部分匹配）。

过滤不会改变数据；清空下拉框或搜索框即恢复完整列表。

## 使用连接器

1. 打开一个网关设备，选择「连接器」页签。
2. 点击**新增**，选择（或输入）连接器类型，可选地指定类型版本。
3. 可选地选择**配置模板**（仅列出携带连接器模板的配置），以及/或者绑定一个子设备。
4. 用结构化表单标签页（或原始 JSON 标签页）编辑配置对象与点位映射；两者合法前保存按钮保持禁用，然后保存。
5. 可按需编辑或删除某行。删除成功响应表示 desired 删除和持久删除意图已提交，并不表示边缘已确认移除；在当前协议下 NL 会持续重放该显式删除。

若要定义可复用的连接器模板，请编辑设备配置并填写其连接器模板字段，参见「设备配置」帮助主题。

## 存活探测（[健康检查](help:glossary/health-check)）

绑定单个子设备的连接器可以运行应用层存活探测。在连接器抽屉中打开「存活探测」开关并填写：

- **探测资源** —— 用于 ping 的南向读资源/命令名。
- **探测间隔** —— 探测节奏（毫秒，默认 30000）。
- **读超时** —— 单次探测超时（毫秒），必须小于间隔（默认 5000）。
- **连续失败阈值** —— 连续失败多少次后标记绑定设备离线（默认 3）；之后首次成功即恢复在线。

网关周期性读该资源并**丢弃读数**——探测绝不产生遥测或影子数据。在线/离线由网关自主
宣告，如同运维人员在现场总线上观察到的一样。策略挂在连接器**实例**上（不是 profile
模板），因为探活是每设备的运维决策；关闭开关即停止探测。
