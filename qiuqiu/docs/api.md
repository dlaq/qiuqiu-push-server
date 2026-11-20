# API 与兼容性说明

## 通用约定

- 兼容入口位于 `https://push.<域名>`；ntfy 原生入口位于 `https://ntfy.<域名>`。
- 每种协议使用后台签发的对应类型凭证。原平台的 SendKey、Token、UID、TopicId 不能直接使用。
- 兼容调用在认证、目标解析后生成同一个消息结构，由 ntfy 发布；Gateway 不发送 FCM，不调用原平台服务。
- 标题可为空；正文不能为空，最长 40000 字符且不超过 65535 个 UTF-8 字节。超限返回明确错误，不静默截断。HTML 经过清理；通知摘要及详情不执行脚本。
- Query、JSON、FORM 均支持中文与标准 URL 编码。JSON/FORM 字段覆盖 Query；路径凭证优先于同名参数，使用 Authorization 时若与已解析凭证冲突则拒绝。
- 原生请求可通过 `Authorization: Bearer TOKEN` 认证；其余协议使用各自标准字段或路径，也接受相同 Bearer 凭证。
- 成功表示持久化受理或 ntfy 发布成功，均不等于手机已经显示。后台消息记录区分 `accepted`、`published`、`failed`，每个设备有独立投递记录，最多三次投递尝试。
- 新请求即使正文相同也不会按内容去重。球球原生接口支持 `Idempotency-Key`：同一凭证、同一键及相同规范化内容返回原消息 ID；同一键内容不同返回 409。Android 还通过消息 ID/序列元数据去重，避免队列重试重复提醒。
- 单个已识别的凭证每分钟最多 120 次 Gateway 发送请求；后台登录/配对入口另有限流。ntfy 原生入口也受 ntfy 的配额限制。

## 路由矩阵

| 协议 | GET | POST JSON | POST FORM | 路径 |
|---|---|---|---|---|
| 球球原生 | 支持 | 支持 | 支持 | `/api/v1/push` |
| Server酱 Turbo | 支持 | 支持 | 支持 | `/sctapi/{SENDKEY}.send` |
| Server酱³ | 支持 | 支持 | 支持 | `/sc3/{UID}/send/{SENDKEY}.send` |
| PushPlus | 支持 | 支持 | 支持 | `/send`、`/send/{token}` |
| Bark | 支持 | 支持 | 支持 | `/bark/{key}/{body}`、`/bark/{key}/{title}/{body}`、`/bark/{key}/{title}/{subtitle}/{body}`、`/push` |
| WxPusher | 支持 | 支持 | 支持扩展 | `/api/send/message` |
| PushBear 旧接口 | 支持 | 支持扩展 | 支持扩展 | `/pushbear/sub` |
| ntfy | 原生 Webhook/读取 | 原生 JSON 发布 | 原生行为 | 独立子域名完整透传，POST/PUT、缓存、Actions、附件、认证 |

非法参数为 HTTP 400，非法凭证 401，越权 403，记录冲突 409，限流 429。兼容响应带 `code`、`msg`/`message`；Server酱成功码 0，PushPlus/Bark 成功码 200，WxPusher 成功码 1000。额外 `warnings` 字段说明无法对应的能力。

### 球球原生

`POST /api/v1/push`：支持 `token`、`topic`、`device_ids`、`business_type`、`title`、`body`、`content_type`（text/markdown/html）、`priority`（1–5）、`icon`、`sound`、`click`、`tags`。

`business_type`/`icon` 别名为 server、security、email、task、network、custom；声音别名为 default、alarm、soft、silent。别名不存在会报错，不接受远程图标作为本地通知图标。使用默认声音时，APP 可按业务选择自己的默认声音，再由 Android 系统渠道设置决定最终表现。

不传目标时使用密钥绑定的目标。传入设备或 Topic 时只能进一步限制在授权范围内，不扩大权限。Topic 群发和设备列表取并集，同一个设备只投递一次。

返回 HTTP 202：`{"message_id":"uuid","status":"accepted","warnings":[]}`。

### Server酱 Turbo / SC3 / PushBear

- `title` 优先，`text` 是旧版标题别名；`desp` 为 Markdown 正文，缺少时使用 `short`。
- `tags` 支持 `|` 或 `,` 分隔，也支持 JSON 数组。
- 提供本地 `url`/`click` 扩展字段用于点击跳转。
- SC3 凭证包含本地数字 UID 前缀。路径中的 UID 必须与签发时返回的 `sc3_uid` 一致，不能填原平台的用户编号。
- PushBear 通过 `sendkey`、`text`、`desp` 兼容历史请求；使用球球的 Server酱类型凭证。并不表示原 PushBear 服务恢复。

成功响应包含 `code:0`、`message:"成功"` 和 `data.id`。

### PushPlus

支持 token、title、content、topic、template（txt/html/markdown）、channel、option、timestamp、to、callbackUrl、pre，以及 url/click 扩展字段。

- `topic` 是自己的本地业务 Topic；`to` 是本地 UID 或设备 ID，多个目标可用数组或逗号分隔。
- `timestamp` 是毫秒失效时间；已过期请求被拒绝，投递时也检查失效。它不作为消息创建时间，也不自动去重。
- channel/option/pre 接收后给出兼容告知；所有消息仍进入 APP，不调用微信、短信渠道，不执行原平台预处理代码。
- callbackUrl 必须是管理员配置的精确 HTTPS URL。禁止重定向、私网和本地地址。回调载荷为 `{"messageId":"uuid","status":"published|failed","meaning":"server publication result"}`，仅表示服务端发布结果；当前回调采用一次有界尝试，失败写入审计，没有设备显示回执或回调重试承诺。

响应 `code:200`、`msg:"请求成功"`、`data.messageId`。

### Bark

JSON 使用 device_key、title、subtitle、body、group、sound、icon、url、level、badge、volume、call。

- subtitle 作为正文前缀；group 是通知分组，不要求同名 Topic；group 为已知业务别名时同时选择该业务。
- sound/icon 只接受本地别名。level 的 passive 映射低优先级，timeSensitive/critical 映射高优先级，不绕过系统勿扰权限；active 为普通通知。
- badge/volume/call 接收并返回能力限制提示；不伪造 iOS 角标、强制音量或持续响铃。
- URL 路径使用百分号编码；编码的斜杠只在切分路径后解码，`+` 保留为路径中的加号。
- 不支持 Bark 的 APNs 注册、iOS 密文推送等非本规格书能力。

响应 `code:200`、`message:"success"`、timestamp 与球球消息 ID。

### WxPusher

支持 appToken、content、summary、contentType（1 文本/2 HTML/3 Markdown）、topicIds、uids、url、verifyPayType。

- uids 支持本地用户 UID 或授权设备 ID；topicIds 为自己创建的数字 TopicId。GET 可使用重复参数、逗号列表或 JSON 数组字符串。`uid` 单值也兼容。
- UID 和 TopicId 同时指定时合并并去重。必须至少指定一个目标。
- verifyPayType 不执行付费订阅过滤，非零值返回能力告知。
- `messageContentId` 是本地数字内容 ID，同时扩展返回 UUID `message_id`。服务端受理码 1000 不代表 APP 已显示。仅 Topic 群发且无显式 UID 时 `data` 可为空数组，消息 ID 仍会返回。

## ntfy 原生与隔离 Topic

在后台签发 **ntfy 原生**类型凭证，完整密钥显示一次，并返回可发布的 `native_topics`：设备收件箱，或隔离的 Topic 群发入口。使用返回的物理 Topic 名称，不自行猜测。

```sh
curl -X POST 'https://ntfy.example.com/后台返回的Topic' \
  -H 'Authorization: Bearer YOUR_NTFY_TOKEN' \
  -H 'Title: 服务器告警' -H 'Priority: urgent' \
  -H 'Click: https://example.com/server' \
  -d '服务器 CPU 温度异常'
```

设备定向请求直接发布至收件箱；群发请求先按 ntfy 原协议发布，再通过持久化队列分发到绑定设备。保留标准消息字段、Actions、附件引用及序列更新。绑定设备拥有所订阅群发 Topic 的读取权限，附件也通过 ntfy 正常鉴权读取。移除订阅时撤销该读取权限。

ntfy 子域名完整保留 `/send`、`/push` 等同名原生 Topic，不被兼容路由占用。push 子域名也透传不与 api/send/push/bark/sctapi/sc3/pushbear/admin/assets/healthz 等保留前缀冲突的原生 Topic；发生冲突时必须使用独立 ntfy 子域名。

原生发布记录可由管理员查看；通过后台签发的原生凭证还会写入所属用户的统一消息记录。手工通过 ntfy CLI 创建的账户未与球球用户关联，只有管理员原生发布记录，不自动绑定球球设备。

## 管理 API

通过 `/api/v1/login`（POST name/password）取得 12 小时 Bearer 会话；不使用 Cookie。`/me` 查看本用户，`/logout` 注销。

`/api/v1/manage/` 下：GET users/apps/devices/topics/bindings/keys/messages/deliveries/stats/audit/native；POST users/apps/topics/bindings/keys/pairings/test；POST keys/reset；PATCH users/devices/keys 禁用；DELETE bindings 取消绑定。users 和原生全局记录仅管理员可访问，其余强制按会话用户隔离。消息、投递和审计记录支持 limit（1–500）与 offset。

密钥 POST 请求：`{"kind":"native","label":"监控","targets":{"device_ids":["DEV_..."],"topic_ids":[1]}}`。可选 app_id 必须属于自己；targets 不能空。设备配对：先 POST manage/pairings，再 POST `/api/v1/pair/claim`，正文 code/name。配对响应包含一次性显示的 ntfy 读取 Token，客户端应安全存储，不写日志。

## 接入边界

调用方必须支持修改 Base URL 或自定义 Endpoint；硬编码官方域名的程序不会自动切换。响应形状兼容常见调用方式，不能保证所有第三方 SDK 的附加接口、付费功能、返回类型假设都被完整复刻。兼容范围以本文、自动化测试和原规格书矩阵为准。
