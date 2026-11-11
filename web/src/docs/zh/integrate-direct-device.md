<!-- Copyright (C) 2026 YIQISOFT; SPDX-License-Identifier: Apache-2.0 -->

# 直连设备接入

本篇面向**设备/边缘开发者**，说明如何让一台**直连设备**（固件直接与 NereidLink
通信，中间不经过网关）接入平台。它不是控制台操作手册。

- **传输方式：** MQTT 3.1.1 / 5.0（首选）或 HTTPS 长轮询（受限网络回退）。
- **载荷格式：** JSON，UTF-8。
- **官方 SDK：** Go —— `github.com/nereidai/nereidlink/sdk/device/go`。它是一个
  精简的组帧/传输库，采集节奏与重连策略完全由固件掌握。其他语言可直接按本文档
  的线协议自行实现。

## 1. 平台侧前置条件

固件连接之前，运维人员必须先在控制台完成准备：

1. 创建设备并绑定**[设备配置](help:glossary/profile)（profile）**，其中的测点描述符声明了点名、值类型
   和单位。
2. 为设备创建至少一种**凭据**，共三类：
   - `TOKEN` —— 单一不透明访问令牌，作为 MQTT 用户名、密码留空（HTTP 则放入
     `Authorization` 头）。
   - ID + 密钥 —— 作为 MQTT 用户名/密码或 HTTP Basic 认证。
   - X.509 客户端证书 —— 双向 TLS。
3. 没有有效凭据的设备无法建立会话。

遥测键名应与 profile 中声明的测点键一致。平台绝不猜测单位或类型：只有描述符
定义的测点才会进行转换/标注单位；未声明的标量键仍会存储，但不做任何变换。

## 2. MQTT 快速开始（Go SDK）

```bash
go get github.com/nereidai/nereidlink/sdk/device/go@latest
```

```go
package main

import (
    "context"

    "github.com/nereidai/nereidlink/sdk/device/go"
    "github.com/nereidai/nereidlink/sdk/device/go/devicemqtt"
)

func main() {
    ctx := context.Background()

    client, err := devicemqtt.Connect(ctx, devicemqtt.Options{
        BrokerURL:  "tcp://nereidlink.example:1883", // 或 tls://...:8883
        Credential: device.Token("device-access-token"),
        ClientID:   "sensor-001",
    })
    if err != nil {
        panic(err)
    }
    defer client.Disconnect(200)

    // 接收下行命令并回复。
    _ = client.SubscribeCommands(ctx, 1, func(cmd device.Command) {
        // 下行 topic：v1/me/cmd/<name>；回复路径使用命令 ID。
        _ = client.RespondCommand(ctx, cmd.ID,
            map[string]any{"ok": true}, 1)
    })

    // 发布扁平的标量测量值。
    frame := device.NewFrame().
        SetFloat("temperature", 21.5).
        SetBool("online", true).
        SetString("mode", "AUTO")
    _ = client.PublishTelemetry(ctx, frame, 1)
}
```

另外两种凭据分别由 `device.Basic(id, secret)` 与 `device.ClientCert(...)`
提供。完整可运行示例见 SDK 的 `examples/simple`。

## 3. MQTT topic 契约

直连设备只使用 `v1/me/*` 系列 topic。

### 设备 → 平台

| Topic | 方向 | 载荷 |
|---|---|---|
| `v1/me/telemetry` | 发布 | 标量测量值组成的扁平 JSON 对象 |
| `v1/me/events` | 发布 | 单个领域事件对象或数组 |
| `v1/me/attributes` | 发布 | CLIENT 作用域属性对象 |
| `v1/me/attributes/get` | 发布 | 按需拉取平台属性：`{"scopes":[...],"response_topic"?}`，`scopes` 省略即 SERVER_SCOPE+SHARED_SCOPE |
| `v1/me/shadow/report` | 发布 | reported 补丁：`{"state":{...},"version"?:N}` |
| `v1/me/cmdres/{command_id}` | 发布 | 请求-响应命令的结果 |

### 平台 → 设备（订阅）

| 过滤器/topic | 载荷 |
|---|---|
| `v1/me/cmd/#` | 命令帧；具体 topic 为 `v1/me/cmd/<命令名>` |
| `v1/me/attributes/down` | SERVER/SHARED 属性推送 |
| `v1/me/attributes/get/ack` | 属性按需拉取应答：`{"scopes":{"SERVER_SCOPE":{...},"SHARED_SCOPE":{...}}}` |
| `v1/me/shadow/desired` | 完整 desired 状态：`{"version":N,"desired":{...}}` |

下行帧均为 QoS 0、非 retained。上行 QoS 由固件选择，建议 QoS 1。

### 遥测帧

```json
{
  "ts": 1728374400000,
  "temperature": 21.5,
  "online": true,
  "mode": "AUTO"
}
```

- 每个值必须是 JSON 数字、布尔或字符串；`null`、嵌套对象和数组在发送前即被
  拒绝。
- `ts` 可选（epoch 毫秒）。省略时由平台盖上到达时间。
- 一帧中的每个测量键各产生一条存储记录。

### 属性与作用域

属性是相对静态的键/值配置或元数据，不是遥测：设备只通过 `v1/me/attributes` 上报 **CLIENT** 作用域；平台管理 **SERVER_SCOPE** 与 **SHARED_SCOPE**。

当前版本中，SERVER_SCOPE 和 SHARED_SCOPE **都会下发给设备**：在线时在 `v1/me/attributes/down` 收到增量，帧为 `{"scope":"...","data":{"键":值}}`；每次（重）连后应发布一次 `v1/me/attributes/get` 补齐离线期间错过的变更，并用 `get/ack` 中每个 scope 的全量键值整体替换本地缓存。请注意这与经典 ThingsBoard 语义不同：经典模型里 SERVER_SCOPE 对设备不可见，只有 SHARED_SCOPE 可见；当前实现不要假设 SERVER_SCOPE 为私有。下行 `data` 中值为 JSON `null` 表示该键已被平台删除，设备应移除本地缓存键（不要保存 null）。请求中的 `scopes` 省略表示同时拉取两个平台作用域；请求 CLIENT 或未知作用域会被拒绝。

### 设备影子（desired / reported）

影子按设备 UUID 保存平台期望的 **desired（目标态）** 与设备实际上报的
**reported（实际态）**，用来区分“我要它进的状态”和“它实际进到的状态”。只有需要
持续维持目标态并确认最终达到的状态才走影子；一次性动作用命令，发后不管的配置用
SHARED 属性。

- **desired 永远是全量。** 在 `v1/me/shadow/desired` 收到的是完整 map：
  `{"version":N,"desired":{...}}`，必须整体替换本地 desired，切勿按增量合并。帧里
  不含 tenant/device 字段——连接本身已标识设备。desired 变化时、以及设备 ONLINE
  （重）连且 `desired_version > 0` 时，都会重推一次全量。
- **reported 是补丁。** 发布到 `v1/me/shadow/report`：
  `{"state":{...},"version"?:N}`；`state` 按顶层键合并。带上 `version` 表示确认你
  刚应用的 desired 版本。值为 JSON `null` 时删除对应顶层键；值可以是标量、对象或
  数组。
- 平台计算 `delta`（reported 与 desired 不一致的键）并标记 `in_sync`，二者都可通过
  REST/WebSocket 读取。

### 媒体：图片、视频、文本等二进制

二进制值绝不进入时序行（无 bytea 列、不内联载荷）。字节存入平台 blob 后端，点上只保留
字符串引用 `blob://<key>`，读侧把引用解析为短时效签名 URL。提供两种上报方式。

**方式一 —— 帧标记（遥测帧内 base64）。** 将标量值替换为如下对象：

```json
{
  "photo": {
    "blob": true,
    "content_type": "image/jpeg",
    "filename": "camera-01.jpg",
    "data": "/9j/4AAQ..."
  },
  "temperature": 23.4
}
```

标记字段：

| 字段 | 必填 | 含义 |
|---|---|---|
| `blob` | 是 | 必须为布尔值 `true`，其他值该字段被拒绝。 |
| `data` | 是 | 文件字节的 **base64 编码**。不接受明文：`"HELLO WORLD."` 必须写成 `"SEVMTE8gV09STGQu"`；空数据被拒绝。 |
| `content_type` | 否 | 字节的媒体类型。省略（或传 `""`）时平台从内容检测类型；平台不猜测单位，但会存储/检测媒体类型。 |
| `filename` | 否 | 原始文件名，仅展示用，不影响对象键。 |

接受的 base64 变体：标准或 URL-safe 字母表、有无填充均可
（`SEVMTE8gV09STGQu`、`SEVMTE8gV09STGQ`、`SEVMTE8gV09STGQu`）。
标记在 MQTT（`v1/me/telemetry`）与 HTTP（`POST /v1/me/telemetry`）上均生效，
可与普通标量值混在同一帧。

**方式二 —— 二进制直传（无需 base64）。**

```bash
curl -X POST "http://<host>/v1/me/blob?key=photo" \
  -H "Authorization: Bearer <device-token>" \
  -H "Content-Type: image/jpeg" \
  --data-binary @camera-01.jpg
```

请求体为原始文件，直接流式落入 blob 存储。`key` 查询参数（默认 `blob`）是测量名，
必须匹配 `[A-Za-z0-9_.-]{1,128}`；对象键始终由服务端生成。响应：

```json
{"published":1,"key":"media/<tenant>/<device>/<uuid>",
 "uri":"blob://media/<tenant>/<device>/<uuid>","size":48213}
```

**大小与类型策略**（平台配置，服务端统一执行一次）：

| 配置项 | 默认值 | 含义 |
|---|---|---|
| `blob.media_max_bytes` | `10485760`（10 MiB） | 单个媒体对象大小上限。 |
| `blob.media_allowed_types` | `image/` | 允许的内容类型；以 `/` 结尾为类型前缀（image/、video/），否则精确匹配。 |

超大对象返回 `413`；不允许的类型返回 `415`（帧标记）或被拒绝；blob 禁用时标记字段直接
报错——绝不内联字节。

**读取媒体。** 引用就是点的字符串值 `blob://media/<tenant>/<device>/<uuid>`。

- 设备自身：`GET /v1/me/blob?key=<key>`，仅服务属于已认证设备的对象，支持 HTTP Range、
  ETag 和条件请求。
- 控制台/用户：`GET /v1/tenants/{tenant}/blob/sign?key=<key>`（权限 `device:read`）
  → `{"url","size","content_type"}`。URL 为 S3 预签名或 HMAC 签名的 `/v1/blobs/get`，
  有效期约 15 分钟。控制台按类型渲染：图片缩略图、视频播放器、文本/JSON/XML 文本查看器
  （内联前 256 KiB）、其他类型下载。

### 命令帧（平台 → 设备）

```json
{
  "id": "01J8...",
  "name": "set-led",
  "direction": "write",
  "params": { "color": "green" }
}
```

- **topic 后缀是命令 `name`**（如 `v1/me/cmd/set-led`）；**回复 topic 后缀
  是命令 `id`**（`v1/me/cmdres/01J8...`）。两者切勿混用。
- `direction` 为 `read` 或 `write`，表示要执行的南向操作，与"是否需要回包"
  正交。固件必须严格按字段执行读/写，不得根据命令名自行推断；read 也可能携带
  `params`（查询参数）；one-way 帧不会带 `direction=read`。
- `params` 是经过平台目录校验的 JSON 文档，原样送达。
- 对于**请求-响应**命令，向 `v1/me/cmdres/{id}` 发布任意 JSON 结果；对于
  **单向**命令，不要回复。
- 命令无法执行时（请求-响应模式）仍应回复一个描述失败的 JSON：平台只要收到
  关联响应就会标记 `SUCCEEDED`，因此业务失败必须显式带上错误字段，例如
  `{"error": "busy"}`。

### 领域事件

```json
{
  "event_type": "door_forced",
  "severity": "warning",
  "ts_ms": 1728374400000,
  "payload": { "zone": 3 }
}
```

`severity` 取值 `info`、`warning`、`error`、`critical`（默认 `info`）。平台
不承载任何业务逻辑：`event_type` 与 `payload` 均为租户自定义的不透明数据。

## 4. HTTP 传输

在只允许 HTTPS 的网络中，设备通道提供：

- `POST /v1/me/telemetry` —— 上传一个扁平遥测帧（支持 base64 媒体标记，见"媒体"一节）。
- `POST /v1/me/blob?key=<测量名>` —— 上传原始媒体字节；
  `GET /v1/me/blob?key=<key>` —— 下载本设备对象（支持 Range）。
- `GET /v1/me/commands` —— 一次长轮询等待；返回下一条排队命令，或在等待窗口
  耗尽后返回空信封。
- `POST /v1/me/cmdres/{command_id}` —— 提交命令结果。
- `POST /v1/me/shadow/report` —— 上报影子状态。
- `GET /v1/me/ota/tasks`、`GET /v1/me/ota/{task_id}/package`
  （支持 Range 断点续传）、`POST /v1/me/ota/{task_id}/progress` —— 固件升级
  流程。

```go
httpClient, _ := devicehttp.New(devicehttp.Options{
    BaseURL:    "https://nereidlink.example",
    Credential: device.Token("device-access-token"),
})

_, _ = httpClient.PublishTelemetry(ctx,
    device.NewFrame().SetFloat("temperature", 21.5))

for {
    cmd, _ := httpClient.PollCommands(ctx) // 一次长轮询；循环重新挂起
    if cmd != nil {
        _ = httpClient.RespondCommand(ctx, cmd.ID,
            map[string]any{"ok": true})
    }
}
```

认证使用同一套凭据（令牌放入 `Authorization`）。

## 5. 命令的时效语义

命令按设备在平台**命令目录**中逐一声明，未声明的命令名无法下发（平台直接以
404 拒绝）。每条声明选择：

- **交互方式（interaction）：** `one-way`（发后即忘，终态 `DELIVERED`）或
  `request-response`（等待一个关联结果，随后 `SUCCEEDED` 或 `TIMEOUT`）。
- **时效（timeliness）：** `realtime`（设备离线立即得到 `NOT_CONNECTED`）或
  `persistent`（排队等待重连后投递；超过 TTL 仍未投递则终态 `EXPIRED`）。

固件侧无需配置这些，只需判断该命令是否需要回复。

## 6. 接入检查清单

- 控制台已创建设备、绑定 profile，并签发凭据写入固件。
- 固件使用正确凭据和唯一 client ID 连接。
- 遥测帧为扁平标量 JSON，键名与 profile 描述符一致；采样时间有意义时带上
  `ts`。
- 二进制媒体走 blob 路径：base64 帧标记或 `POST /v1/me/blob`；标记的 `data`
  绝不写明文，并确认内容类型在白名单（`blob.media_allowed_types`）内。
- 固件订阅 `v1/me/cmd/#`，区分 `name`（topic）与 `id`（回复），且对所有
  请求-响应命令都回复——失败场景也不例外。
- 重连由固件负责（MQTT SDK 提供自动重连选项）；重连后使用新的时间戳继续上报。
