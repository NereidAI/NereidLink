<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 网关设备接入

本篇面向**[网关](help:glossary/gateway)/边缘节点开发者**：网关节点通过**一条** MQTT 连接接入
NereidLink，并在其后管理大量**[子设备](help:glossary/subdevice)**（Modbus/ONVIF/S7 等现场总线或本地设备
服务）。

- **传输方式：** MQTT 3.1.1 / 5.0 over TLS（mTLS 可选）。
- **载荷格式：** JSON，UTF-8。
- **官方 SDK：** Go —— `github.com/nereidai/nereidlink/sdk/device/go` 的
  `gateway` 包；完整参考程序见 `examples/gateway`。其他语言可直接按本文档的
  线协议实现。
- 平台不理解任何南向协议。平台子设备 UUID 与本地设备之间的映射完全由网关
  负责。

## 1. 平台侧前置条件

1. 网关本身在平台注册为**网关类型**设备，并拥有自己的凭据（ID+密钥、令牌或
   X.509 证书）。
2. 每台子设备都在控制台注册并**绑定到该网关**。网关只能操作绑定到自己的子
   设备；发往其他网关或未挂载子设备的帧会被丢弃。
3. 子设备的 profile 声明遥测点名称；命令必须先在该设备的命令目录中声明，才能
   下发。

## 2. 会话生命周期

状态机：`UNIDENTIFIED → NEGOTIATED → CLOSED`。

1. 建立 MQTT 连接时预先注册 LWT 到 `v1/gateway/goodbye`。
2. 订阅 hello 响应 topic（或使用 MQTT 5 Response Topic）。
3. 发布 `v1/gateway/hello`；协商必须在 10 秒内完成。
4. 只有收到成功 ack 后才能使用网关数据 topic。协商完成前发出的帧一律丢弃。
   每条连接只协商一次。

### Hello（网关 → 平台），topic `v1/gateway/hello`

```json
{
  "v": 1,
  "edge_id": "edge-001",
  "agent_version": "2.3.1",
  "proto": {"min": "1.0", "max": "1.4"},
  "features": {
    "required": ["telemetry", "presence"],
    "optional": ["ota", "shadow", "blob-relay", "command", "secrets", "profiles"]
  },
  "profile_formats": ["edgex-device-profile/3", "nereid-neutral/1"],
  "connectors": [
    {"type": "modbus", "type_version": "2.1",
     "config_schema": {"type": "object", "required": ["host"],
       "properties": {"host": {"type": "string"}, "port": {"type": "integer"}}}}
  ],
  "client_time": 1727360000123
}
```

- `features.required` 必须全部被服务端支持，否则协商失败
  （`incompatible_features`）；`features.optional` 中服务端不支持的项会被
  降级移除。可用特性：`telemetry`、`presence`、`ota`、`shadow`、
  `blob-relay`、`command`、`secrets`、`profiles`。
- `profile_formats`（可选）按**偏好顺序**列出本网关能落地的设备 profile 文档
  格式（见 §10.1）；ack 的 `profile_formats` 回显协商后的**交集**。省略或为空
  表示仅支持中立格式。
- `connectors[]` 可选，用于登记本网关能运行的连接器类型的**内联自包含** JSON
  Schema（draft 2020-12，不支持外部 `$ref`）。单条 schema 非法不会导致握手
  失败，其结果在 ack 中逐条回告。
- `agent_version` 是固件版本字符串，与协议版本相互独立。

### Ack（平台 → 网关），topic `v1/gateway/hello/ack`

```json
{
  "session_id": "s_...",
  "status": "ok",
  "selected": "1.4",
  "server": {"min": "1.0", "max": "2.0", "version": "nereidlink-..."},
  "features": ["telemetry", "presence", "command"],
  "profile_formats": ["edgex-device-profile/3"],
  "limits": {"heartbeat_s": 30, "hello_timeout_s": 10,
             "upload": {"max_mb": 256, "qps": 20}},
  "time": {"server_time": 1727360000456, "offset_ms": -312, "clock_warning": false}
}
```

Ack 状态：`ok`、`auth_failed`、`incompatible_version`、
`incompatible_features`、`quota_exceeded`。心跳间隔以 `limits.heartbeat_s`
为准。每次重连后服务端状态都重置为 `UNIDENTIFIED`：必须重新 hello、重新订阅、
重新上报状态。

## 3. 在线探测：心跳、goodbye 与 LWT

- 按协商间隔向 `v1/gateway/heartbeat` 发布心跳：

```json
{"seq": 42, "client_time": 1727360060000}
```

- CONNECT 时将 `v1/gateway/goodbye` 预注册为 MQTT 遗嘱。优雅停机时显式发布
  该消息并带 `planned: true`：

```json
{"reason": "upgrade", "planned": true}
```

  broker 投递的 LWT 不带 `planned` 标志，因此崩溃停机与计划内停机可区分。
- 心跳缺失会驱动平台看门狗（`HEALTHY → SUSPECT → DOWN`），并级联导致其挂载
  子设备离线。goodbye（计划内或 LWT）会抑制可疑计时器。

### 3.1 Link episode 补发（`v1/gateway/linkhealth`）

网关本地运行 link 看门狗，每次非计划断连在重 hello 成功后补发该 episode（QoS1）：

```json
{
  "edge_id": "<网关UUID>",
  "session_id": "s_…",
  "down_at": "2026-10-10T03:12:44Z",
  "up_at": "2026-10-10T03:12:49Z",
  "reason": "transport_down"
}
```

- `edge_id` 必须与本连接的网关设备 id 一致；`down_at`/`up_at` 为 RFC3339（边缘时钟即
  ground truth），`up_at` 不得早于 `down_at`；`session_id` 可选。
- 平台会校正匹配的 lease episode，或为短于 lease 窗口的闪断记录一条已闭合、无告警的
  episode，并在 `v1/gateway/linkhealth/ack` 回 `accepted` / `duplicate` / `rejected`。
  收到三者之一即停止重传；未收到 ack（服务端瞬时错误）才重试。
- 积压补发即按时间最旧在前发 QoS1 帧，平台按 `(edge_id, down_at)` 去重。10 分钟内 ≥5 条
  未匹配 episode 会打开 flapping 告警，安静满 10 分钟后自动闭合。

## 4. 子设备 [presence](help:glossary/presence)

| Topic | 方向 | 载荷 |
|---|---|---|
| `v1/gateway/connect` | 发布 | `{"devices": ["<uuid>", ...]}` |
| `v1/gateway/disconnect` | 发布 | `{"devices": ["<uuid>", ...]}` |
| `v1/gateway/manifest` | 发布 | 每 5 分钟一次的全量状态对账 |

子设备挂载/卸载时批量上线下线。manifest 是权威的周期性全量清单，平台据此对账
desired 与 reported presence：

```json
{
  "devices": ["<uuid-1>", "<uuid-2>"],
  "connectors": [
    {"spec_id": "ci-...", "revision": 3, "config_hash": "..."}
  ],
  "missed_cycles": 0
}
```

- `connectors` 字段的存在与否有明确语义：**省略该字段**表示跳过连接器对账；
  显式发送空数组 `[]` 表示"当前零个连接器实例"。
- 每次都必须列出当前挂载的全部子设备 UUID；manifest 是全量状态，不是增量。

## 5. 子设备[遥测](help:glossary/telemetry)

向 `v1/gateway/telemetry` 发布批量数据。载荷是以子设备 UUID 为键的映射，每个
值就是直连设备上报的那种扁平标量帧，可带可选 `ts`（epoch 毫秒）：

```json
{
  "07704cf9-...": {"ts": 1728374400000, "MACAddress": "AA:BB:CC:00:11:22"},
  "089fbb69-...": {"temperature": 23.1, "online": true}
}
```

规则与直连设备完全相同：值只能是数字/布尔/字符串（不允许嵌套对象或数组）；
每个键一条存储记录；未声明键照常存储但不做描述符转换。值也可以是
`{"blob":true,...}` 标记（见直连设备集成）：媒体存入
`media/<租户>/<子设备>/...`，点变为 `blob://` 引用。

CLIENT 作用域属性走 `v1/gateway/attributes`，形状为 `{device: {key: value}}`；
SERVER/SHARED 属性推送从 `v1/gateway/attributes/push` 接收，帧为
`{"device":"...","scope":"...","data":{...}}`。每次（重）连后，网关应为每个已挂载
子设备发布一次 `v1/gateway/attributes/get`（`{"device":"...","scopes":[...],"response_topic"?}`，
`scopes` 省略即 SERVER_SCOPE+SHARED_SCOPE），并用 `v1/gateway/attributes/get/ack`
中每个 scope 的全量键值整体替换本地缓存。推送 `data` 中值为 JSON `null` 表示该键已删除，
应移除本地缓存（不要保存 null）。当前版本 SERVER_SCOPE 与 SHARED_SCOPE **都会下发给设备**
（不同于经典 ThingsBoard 中 SERVER_SCOPE 平台私有），不要假设 SERVER_SCOPE 为私有。
网关**自身**的属性走直连设备 topic：`v1/me/attributes`、`v1/me/attributes/down`、
`v1/me/attributes/get`、`v1/me/attributes/get/ack`——网关本身也是一个设备，网关整机的
运行旋钮（如日志级别、追踪开关）从这里下发，而不是 `v1/gateway/...`。

## 6. 子设备命令

命令按子设备在平台命令目录中逐一声明；网关只负责在本地总线上执行。

### 下行（平台 → 网关），topic `v1/gateway/command`

一个订阅服务所有子设备，由帧中的 `device` 字段选择目标：

```json
{
  "command_id": "01J8...",
  "device": "07704cf9-...",
  "interaction": "request-response",
  "timeliness": "realtime",
  "method": "DeviceInformation",
  "direction": "read",
  "params": {}
}
```

- `interaction`：`one-way`（无需回复）或 `request-response`（恰好一个关联
  回复）。
- `timeliness`：`realtime`（离线快速失败）或 `persistent`（平台侧排队；网关
  只会看到在线时的实时帧）。
- `method` 是命令目录中的命令名；`params` 是经目录校验、原样送达的 JSON。
- `direction`：`read` 或 `write`，表示南向操作语义（读资源 / 写资源），与
  `interaction` 正交。网关**必须严格按 `direction` 执行 GET/写操作，不得根据
  method 自行推断**；read 同样可能携带 `params`（查询参数）。`one-way` 命令
  不会带 `direction=read`。

### 上行（网关 → 平台），topic `v1/gateway/command/response`

```json
{
  "command_id": "01J8...",
  "device": "07704cf9-...",
  "data": {"manufacturer": "Acme", "model": "Cam-9", "firmware": "1.4.2"}
}
```

- `command_id` 与 `device` 原样回显；`data` 为任意 JSON。
- `one-way` 命令不发送任何回复。
- 本地执行失败时（现场总线不可达、读错误、超时），请求-响应命令**仍须回复**，
  把错误放进 `data`，例如 `{"error": "no response from field bus"}`。
  完全不回复会导致平台侧 `TIMEOUT`。
- 响应只接受来自挂载该子设备的同一网关。

Go SDK 用法：

```go
_ = gw.SubscribeCommands(ctx, 1, func(cmd device.GatewayCommand) {
    if cmd.Interaction != device.InteractionRequestResponse {
        return // 单向命令：静默执行
    }
    result := executeOnFieldBus(cmd.Device, cmd.Method, cmd.Params)
    _ = gw.RespondCommand(ctx, cmd.CommandID, cmd.Device, result, 1)
})
```

### 6.1 子设备影子（desired / reported）

影子按**设备 UUID** 存储、与传输方式无关：网关**自身**的影子走直连语义的
`v1/me/shadow/*`；网关代理的子设备影子走下面的批量网关 topic。

- 向 `v1/gateway/shadow/report` 上报一台或多台子设备的 reported。单子设备就是长度为
  1 的数组：

```json
{
  "reports": [
    {"device": "07704cf9-...", "state": {"led": "on", "brightness": 7}, "version": 3},
    {"device": "089fbb69-...", "state": {"rpm": 1200}}
  ]
}
```

  `state` 是按顶层键合并的补丁（值为 JSON `null` 时删除该键）；`version` 可选，用
  于确认刚应用的 desired 版本。逐条独立鉴权、独立入库：`device` 为空、不是 UUID 或
  未 attach、`state` 不是对象或为空时，只丢弃并审计该条，其余条目照常入库。顶层 JSON
  无法解析时整帧丢弃。

- 只需订阅一次 `v1/gateway/shadow/desired`，再按 `device` 分发。与直连通道一样，
  desired **永远是全量**而非增量——对每个具名子设备整体替换本地 desired：

```json
{"desired": [{"device": "07704cf9-...", "version": 3, "desired": {"led": "on"}}]}
```

  desired 变化时、以及子设备 ONLINE 且 `desired_version > 0` 时都会重推全量，无需按
  设备 get。

Go SDK：

```go
_ = gw.SubscribeSubdeviceShadowDesired(ctx, 1, func(frame device.GatewayShadowDesiredFrame) {
    for _, d := range frame.Desired {
        applyLocalDesired(d.Device, d.Version, d.Desired) // 全量替换
    }
})

_ = gw.ReportSubdeviceShadow(ctx, []device.GatewayShadowReport{
    device.NewGatewayShadowReportForVersion("07704cf9-...", map[string]any{"led": "on"}, 3),
}, 1)
```

## 7. 连接器生命周期（desired / reported）

连接器实例以 NereidLink 为系统记录者；网关应用 desired 状态并回报实际运行
状态。

- 只需订阅单一过滤器 `v1/gateway/connectors/#`。desired spec 从
  `v1/gateway/connectors/spec` 接收（`spec_id`、`connector_type`、
  `type_version`、`config`、`mappings`、`health_check`、`revision`），删除从
  `v1/gateway/connectors/delete` 接收。
- 每次 hello 后平台都会**幂等地重发完整 desired 集合，以及一批有界且公平的未确认显式删除**；
  领导者选举出的后台周期扫描会继续分批重放删除。按 `(spec_id, revision)` 去重。NL 会先原子提交
  desired 删除与持久 tombstone，再尝试下发。当前没有 delete-ACK 契约：传输成功、
  REST `204`、超时、普通 `APPLIED` 和 manifest 缺项都不会退休 tombstone。
- 在 `v1/gateway/connectors/status` 上按状态机
  `CREATING → APPLIED / FAILED / DEGRADED` 回报，回显 `spec_id` 与当前 desired
  `revision`；旧 revision 或未知的未来 revision 会被忽略。
- 可选 `stats` 字段（`success_total`、`error_total`、`p50_ms`、`p95_ms`）彼此独立。
  显式零计数是真实读数，缺字段表示不可用；每个被接受的状态都会整体替换而非合并
  旧 stats 投影。`APPLIED` 还可携带 manager GET 的 `observed_revision` 和
  `observed_hash`；该 hash 不是 manifest 配置 hash。

### 实例存活探测（`health_check`）

绑定单个子设备的 spec 可能携带 `health_check`：

```json
{"resource": "camera_status", "interval_ms": 30000,
 "timeout_ms": 5000, "fail_threshold": 3}
```

- 每 `interval_ms` 对该子设备**读一次 `resource`**；读数**一律丢弃**，禁止写入
  telemetry/shadow/任何 Envelope 上报。
- 连续 `fail_threshold` 次失败或超时即在 `v1/gateway/disconnect` 宣告该子设备
  离线；之后首次成功探测在 `v1/gateway/connect` 宣告恢复（只发状态边沿）。
- 字段消失或 spec 删除时停止探测；该机制与 MQTT 传输层 keepalive 相互独立。

控制台侧的操作流程见帮助中心的**连接器**篇。

### 7.1 业务编排展开

[业务实例](help:glossary/business-instance)生成确定性的[业务展开](help:glossary/business-expansion)，把摄像头、pipeline、伴生服务和规则装配映射到同一套连接器 desired/reported 生命周期，不新增第二套连接器传输通道。`inference_binding` 展开物因此仍以普通 `edge-inference` spec 下发，并回报普通连接器状态。

pipeline 注册和仅引用式外部 servable 注册使用完整的 `edge-manager-resource` v1 连接器封套，配置位于嵌套的 `spec.config`；外部条目使用 `entry_id` 和 `id_field=servableId`。manager 的 `APPLIED` 只有同时携带当前 `observed_revision` 和非空 `observed_hash` 才能证明健康；`DEGRADED/manager-entry-missing` 仍为降级。在 NI 提供 runtime-only 的对称资源前，带 definition 的托管 `ExternalServablesJSON` 一律失败关闭。伴生服务订阅仍由其独立回签的投递契约门控。

北向上传与 NI 的 `PublishMode`/`PublishReadings` 分别声明。网关出口必须执行已回签的通道与字段白名单契约，即使错误的北向声明请求 embedding，也必须在出口丢弃。

北向节点还必须提供精确的原始 EdgeX 选择器：`edgex_device_name`、`edgex_profile_name` 和非空的 `edgex_resource_names` 数组。这些名称来自网关上的 EdgeX 名称，不是 NereidLink UUID 或规范名称。NL 将每个 `northbound_channel` 保留为一个贡献输入，再协调出一个网关级 `edge-northbound-filter` v1 连接器，其 `device_id` 为空。每个贡献会生成一条显式 allow 规则，以及同设备／同 profile 的 deny 规则；模式匹配、`PipelineJSON` 和大小写敏感的 `Embedding*` 资源都会被拒绝。不同实例的 active 与 pending 展开共享这个单例。最后一个贡献移除后，NL 会将其静默为 `enabled=false`、`default_action="allow"`、`rules=[]`，而不是硬删除。转发封套携带 `channel` 元数据，`forwarder_id` 仍负责选择 stream 和 sink。

## 8. 可选能力

| 能力 | 下行 | 上行 | 说明 |
|---|---|---|---|
| 按需探测 | `v1/gateway/probe` | `v1/gateway/probe/response` | 按子设备做可达性测试；逐设备或用 `responses[]` 批量回报 |
| 发现 / 自动开通 | `v1/gateway/discovery/result` | `v1/gateway/discovery` | 上报扫描候选，平台将本地 ID 映射为 UUID |
| OTA | `v1/gateway/ota/task` | `v1/gateway/ota/progress` | 外部固件 URL，有序进度状态 |
| Profile 库 | `v1/gateway/profiles/spec` | `v1/gateway/profiles`、`v1/gateway/profiles/status` | 上报本地 profile 供采用；接收库共享下发的 profile（见 §10.1） |
| 属性推送 | `v1/gateway/attributes/push`、`v1/gateway/attributes/get/ack` | `v1/gateway/attributes`、`v1/gateway/attributes/get` | SERVER/SHARED 下发 + CLIENT 上报；`null` 表示删除 |
| 设备影子 | `v1/gateway/shadow/desired` | `v1/gateway/shadow/report` | desired/reported 影子状态 |
| 网关操作 | `v1/gateway/operations/invoke` | `v1/gateway/operations/result` | 同步网关整机动作；见第 9 节 |
| 边端模型推理 | （无——复用既有 topic） | `v1/gateway/manifest`（`models`）、`v1/gateway/connectors/status`、影子 reported `inference` | 上报本地模型库存、运行 `edge-inference` 连接器；见 §8.1 |

只在 hello 的 `features`（会话级特性）中声明实际实现的能力，其余不要发布。

### 8.1 边端模型推理（可选，ADR-0039）

能在本地提供 AI 模型推理的网关通过协商 `inference` 能力并上报模型库存接入。平台不中转
模型文件、也不发起推理调用，只保存上报视图、通过通用连接器把摄像头绑定到模型，并在
模型/运行时/绑定三个状态面产生告警。完整契约见对接协议 v1 §15，边端侧要点：

1. **Hello**：在 `features.optional` 加 `"inference"`，并发送顶层
   `"inference": {"contract": "model/1", "backends": ["kserve", "openai"]}`。只有当 Ack
   回显 `inference` 块时才启用（contract 必须等于 `model/1`；`backends` 为按边缘声明顺序
   的交集）。Ack 未回显时绝不发送 `models` 段，也不支持 `edge-inference` spec。
2. **库存**：在既有 `v1/gateway/manifest` 帧中，与 `devices`/`connectors` 并列加入可选的
   全量 `models[]` 段，按既有节奏发送（变化即发（去抖）、每 5 分钟全量、(re)hello 后
   重放）。字段缺省表示保留平台已存库存不动；发送 `[]` 表示权威的零库存。每项为
   `{id, version, revision?, task, family?, backend, server_family?, model_name?, state, reason?, labels?, active_bindings?}`，
   `state` 取 `EXTRACTING/VALIDATING/READY/FAILED/REMOVED`；模型从非空列表中消失会被平台
   删除。hello 之前或非网关身份不要发送该段。可选的 `server_family` 是具体模型服务器产品
   标识（如 `ovms`/`triton`/`external`），与协议族 `backend` 不同；未知时省略即可，旧平台
   会忽略该未知键。
3. **连接器**：在 hello `connectors[]` 自注册 `connector_type="edge-inference"`、
   `type_version="1"` 及其 config JSON Schema。desired spec 走正常的
   `v1/gateway/connectors/spec`，`device_id` 必填（一台虚拟输出子设备），config 不透明，含
   `source`（ONVIF 托管摄像头：`{device, resource:"snapshot_jpeg", mode?, profile?}`，`mode` ∈ `snapshot`|`stream`；
   stream 映射为联邦 RTSP/ffmpeg，URL 与凭据不出边缘；`profile` 为可选的 ONVIF Media
   ProfileToken（仅 stream），正则 `^[A-Za-z0-9_.:,-]+$`、≤64，缺省为摄像头默认；token 通过
   只读 operation `camera.profiles` v1 枚举（入参 `{source}`，返回
   `{profiles:[{token,name,encoding,width,height,fps}]}`，走 core-command `GetProfiles`，
   回退 `Profiles`，结果不含 URL/地址）；非 ONVIF 硬件走第三个模式 `mode:"url"` 逃生舱，
   不带 device/resource：`{mode:"url", transport, url, secret_name?}`，其中 `transport` ∈
   `rtsp`|`rtsp-udp`|`mpjpeg`|`jpeg`，`url` 长度 8–1024（RTSP 类必须 `rtsp://`，mpjpeg/jpeg
   必须 `http(s)://`，必须带 host，内嵌 userinfo 拒绝并回报 config-invalid），可选
   `secret_name`（≤128，`^[A-Za-z0-9_.:,-]+$`）指向由运维在 NI 密钥库预置的密钥——URL
   会被平台存储，凭据绝不入云）、`model_ref`、`trigger`
   （`event`|`periodic`|`continuous`）/`period_ms`/`max_idle_ms`/`fps`（0.1–60）、阈值、
   证据帧开关 `emit_frame`/`annotate_preview`（布尔，默认 true，取缺省时不下发）、
   stream/直连 URL 的 `ffmpeg_extra`（≤256，白名单 argv：`-re` 或带值的
   -r/-ss/-t/-threads/-fflags/-flags，如 `-fflags nobuffer -flags low_delay`，
   非法回报 config-invalid），站点本地的 `decode_accel`（缺省 `software` 不下发 /
   `vaapi`，仅 ONVIF RTSP 与直连 rtsp/rtsp-udp）及可选 `vaapi_device`
   （`renderD<N>` 或 `/dev/dri/renderD<N>`，缺省由 NI 使用 renderD128），映射为 NI
   `DecodeAccel`/`VAAPIDevice`；VA-API 设备缺失时明确失败且不回退软解，以及
   可选的 `target_device`（默认 `CPU`；可取 `GPU`/`GPU.0`/`NPU`/`AUTO` 或 OVMS 的
   `MULTI:..`/`HETERO:..` 形式，正则 `^[A-Za-z0-9_.:,-]+$`、长度 ≤64——请在自注册的 config
   JSON Schema 中同时声明 `mode`/`profile`/`fps`/`continuous`/`emit_frame`/`annotate_preview`/`ffmpeg_extra`/`decode_accel`/`vaapi_device`/`url`/`transport`/`secret_name`
   能力与该属性；`mode` 缺省等价 snapshot，
   字段缺省等价于 `CPU`）。视频流/直连源无可用信号或 401 时必须回报 `source-no-frame`，不得偷偷切换取流方式。
   `target_device` 是硬性部署要求：做不到
   时必须回报 FAILED + `target-device-unavailable`，不得静默回退 CPU。在
   `v1/gateway/connectors/status` 回报 `APPLIED/DEGRADED/FAILED` 与传输安全的 `reason`；不
   配置 `health_check`，活性由 `max_idle_ms`/`period_ms`/`fps` 自管（`idle-no-inference` 属于
   DEGRADED 而非故障），绑定模型缺失/未 READY 时回报 `model-not-ready`。
4. **运行时健康**：持续刷新设备影子 reported 的 `inference` 块：
   `{state: ONLINE|DEGRADED|OFFLINE|UNKNOWN, endpoint?, protocol, models_ready,
   models_total, active_bindings, checked_at（RFC3339）, probe_interval_s, latency_ms?}`。
   静默超过三个探测周期会被平台判为 UNKNOWN/过期。
5. **结果与自检**：推理结果作为**普通遥测**归属到绑定的虚拟子设备，使用 `detection/v1`
   系统 profile 的规范点（`detection_count`、`top1_label`、`top1_score`、
   `infer_latency_ms`、`detections_json`）；证据图片走既有 Blob 机制。注册 `inference.diag`
   v1 操作以支持按需故障定位。失败原因使用协议 v1 §15.6 的统一错误码表（`model-not-ready`、
   `runtime-unreachable`、`inference-timeout`、`source-no-frame`、`idle-no-inference`、
   `package-invalid` 等）。

## 9. 网关操作（网关整机动作）

操作是作用于网关自身的即时动作（重启、厂商维护例程），与子设备命令（第 6
节）不同。平台不内置任何操作词汇：在 hello `operations[]` 中广告本固件实现的
 catalog descriptor——操作标识名、交互方式（`request-response`/`one-way`）、超时、
 参数/结果 JSON Schema 与风险级（`read`/`normal`/`dangerous`）。

平台对每个网关的描述符**各自存储**（migration 0080）：两台网关广告相同键也互不覆盖，
调用时按**目标网关自己的副本**校验。为保持多厂商机群一致，请遵守下列命名/版本规则：

- `operations[]` 每次 hello 都是该网关当前操作的**全量集合**：后续 hello 未提交的键会被
  对账删除。不说话术的老固件应整体缺省该字段（null）；显式发送 `[]` 表示清空。
- `operation`+`type_version` 即契约：跨网关同名同版本的参数、结果、risk、interaction
  必须完全一致。
- 通用动作使用标准命名空间 `diag.*`（诊断）、`sys.*`（本机信息/维护）、`service.*`
  （托管服务）；厂商动作必须使用 `vendor.<厂商标>.*`，避免与标准键撞名。
- 破坏性契约变更必须换 `type_version`（或换新键，如 `diag.ping.v2`），因为调用帧不选版本；
  同一网关广告多个版本时调用最新一条。
- 控制台按各网关广告聚合，并在同名同版本契约不一致时标记**契约冲突**。

- 订阅 `v1/gateway/operations/invoke`。调用帧含 `id`、`operation`、`type_version`、
  `params`（已校验）与 `timeout_ms`；帧中的 `timeout_ms` 是实际生效时限——取描述符
  默认值与操作员本次下调值的较小者，并钳制在 100–60000 毫秒。在
  `v1/gateway/operations/result` 以相同 `id` 回报 `ok` 以及 `result` 或 `error`。
- 只执行本机广告过的操作；每条请求-响应调用必须在 `timeout_ms` 内回报——不回报
  平台侧记 `TIMEOUT`。单向操作不回报。
- 形如 `secret://<名称>` 的参数在最后一跳已还原为明文；禁止记录日志或持久化。
- 结果中**绝不允许包含密码**：平台不检查结果内容，脱敏由网关负责。
- 调用只在在线时发生：网关离线立即得到 NOT_CONNECTED，帧绝不排队；迟到/重复的
  结果会被丢弃。

```go
_ = gw.SubscribeOperations(ctx, 1, func(inv device.OperationInvoke) {
    out := runVendorOperation(inv.Operation, inv.Params)
    _ = gw.RespondOperation(ctx, device.OperationResult{
        ID: inv.ID, OK: true, Result: out,
    }, 1)
})
```

## 10. Topic 汇总

网关 → 平台：`v1/gateway/hello`、`heartbeat`、`goodbye`、`linkhealth`、
`connect`、`disconnect`、`manifest`、`telemetry`、`attributes`、
`shadow/report`、`command/response`、`ota/progress`、`probe/response`、
`connectors/status`、`secrets/status`、`profiles`、`profiles/status`、
`discovery`、`operations/result`。

平台 → 网关：`hello/ack`、`linkhealth/ack`、`command`、`attributes/push`、
`shadow/desired`、`ota/task`、`probe`、`connectors/spec`、
`connectors/delete`、`secrets/sync`、`profiles/spec`、`discovery/result`、
`operations/invoke`。

上行 QoS 由网关选择（建议 QoS 1）；下行帧均为 QoS 0、非 retained。

### 10.1 设备 Profile 下发（库共享）

租户内任何位置建模或上报的 profile 都会成为**库资产**；平台可把它投递给任何需要
它的网关，无需重新编写（例如 A 网关上报了 `A1`，B 网关直接接收）。在 hello
`features` 中声明 `profiles`，可选地在 `profile_formats` 中给出偏好文档格式；ack
回显协商后的 `profile_formats`。

订阅 `v1/gateway/profiles/spec`。每帧是该网关的**全量期望集**，并在每次 hello 后
重放：

```json
{"tenant_id": "…", "gateway_id": "…", "entries": [
  {"profile_id": "…", "name": "A1", "revision": 2,
   "format": "edgex-device-profile/3",
   "target": {"service": "device-modbus"},
   "document": {"name": "A1", "deviceResources": [ /* 已渲染 */ ]}},
  {"profile_id": "…", "name": "Old", "revision": 1,
   "format": "edgex-device-profile/3", "removing": true}
]}
```

- `document` **已在云端按 `format` 渲染完成**（双方都支持的第一个格式）。目前支持
  `nereid-neutral/1`（始终可用；即你上报时的 `{name,protocol?,points[]}` 结构）与
  `edgex-device-profile/3`（EdgeX `deviceResources`/`deviceCommands`）。
- 幂等落地，以 `(name, revision)` 去重。`target` 为可选、不透明提示（如托管该
  profile 的 device service 名）。
- `removing: true` 时**不携带** `document`：删除本地该 profile。
- 通过 `v1/gateway/profiles/status` 回报：

```json
{"reports": [{"name": "A1", "revision": 2, "status": "APPLIED", "reason": ""}]}
```

  状态 ∈ `APPLIED`、`FAILED`、`REJECTED`、`REMOVED`；失败时在 `reason` 给出简短、
  传输安全的说明。平台侧状态为 `PENDING → DISPATCHED → APPLIED(/FAILED)`，长时间未
  生效或持续失败会触发告警。
- 文档由云端渲染；平台**不会**把 profile 落到你的 device service——那是 agent 的
  职责。

## 11. 接入检查清单

- 网关注册并具备凭据；所有子设备已注册并绑定到该网关。
- 每次（重）连后发送 hello；在 `status: ok` 之前不发布任何网关帧；特性清单与
  实际实现一致。
- CONNECT 前注册 LWT；优雅停机发计划内 goodbye；按 `limits.heartbeat_s`
  发心跳。
- 重 hello 后补发本地看门狗 episode；ack 只订阅一次；收到
  `accepted`/`duplicate`/`rejected` 即停止重传。
- 挂载变化时发 `connect`/`disconnect`；每 5 分钟发全量 manifest，正确处理
  `connectors` 字段的有无语义。
- 遥测批量以 UUID 为键，值只含扁平标量。
- 命令：一个订阅处理全部子设备；每条请求-响应命令都按 `command_id` +
  `device` 回复（失败也要回）；单向命令绝不回复。
- 连接器推送按 `(spec_id, revision)` 去重，并通过状态机回报状态。
- 影子：网关自身影子走 `v1/me/shadow/*`；子设备 reported 批量发往
  `v1/gateway/shadow/report`，只订阅一次 `v1/gateway/shadow/desired` 并按
  `device` 分发；desired 全量替换，切勿按增量合并。
- 在 hello `operations[]` 中广告操作；每条请求-响应调用都在 `timeout_ms` 内按
  `id` 回复 `operations/result`；结果中不含密码。
- 若启用 `profiles`：已订阅 `v1/gateway/profiles/spec`，条目按 `(name, revision)`
  幂等落地，每个已应用（或已删除）条目都在 `v1/gateway/profiles/status` 回报。
