# 球球推送（QiuQiu Push）
# 产品需求与技术开发规格书

**文档版本：** V2.0  
**项目名称：** 球球推送  
**英文名称：** QiuQiu Push  
**产品类型：** 自建 FCM 推送平台 + Android 推送客户端 + 多协议 API 兼容服务  
**目标设备：** OPPO Find N5 国行版，ColorOS 16  
**服务端环境：** Ubuntu 24.04 + Docker  
**开发语言：** Android Kotlin、服务端优先 Go

---

# 一、项目定位

球球推送是一套完全由用户自行部署和管理的推送系统。

它不仅提供 Android APP 接收通知，还要提供一个兼容国内主流推送平台 API 格式的服务端。

任何第三方程序、服务器、自动化工具、监控平台，只要能够发送 HTTP 请求，就可以通过球球推送的 API 将通知发送到用户自己的手机。

项目采用：

- 自建推送服务端。
- 自己的 Firebase 项目。
- Google FCM 作为 Android 系统级推送通道。
- 自定义 Android 客户端。
- 多协议 API 兼容层。
- 统一消息处理和通知管理。

核心原则：

1. 消息由自己的服务端接收和管理。
2. 自己的服务端直接调用 FCM，将消息推送到自己的 APP。
3. 不依赖 WxPusher、Server酱、PushPlus 等第三方推送平台的服务器。
4. 不使用后台 WebSocket 长连接。
5. 不使用 HTTP 定时轮询。
6. 不使用前台服务常驻保活。
7. 不需要 Root、Shizuku、Magisk 或 KernelSU。
8. 支持第三方程序使用多种兼容 API 格式调用。
9. 支持不同业务使用不同通知图标、声音和通知渠道。
10. 支持点击通知打开指定网页。

FCM 是本项目的默认推送传输方式。假设设备已经具备正常的 Google Play Services 和 FCM 网络连接，不将国内 FCM 网络可达性问题作为本项目开发范围。

# 二、产品品牌和 APP 图标

## 2.1 APP 名称

中文名称：球球推送

英文名称：QiuQiu Push

## 2.2 APP 图标

核心视觉形象：卡通毛线球球。

设计要求：

- 一个可爱的卡通毛线团作为主体。
- 毛线具有明显的缠绕纹理和柔软质感。
- 整体形象简洁、亲切、容易识别。
- 采用柔和、明快的配色。
- 可以有可爱的表情，但不能影响毛线球主体辨识度。
- 不出现其他推送平台的 Logo。
- 不出现文字。
- 适合 Android 桌面图标和自适应图标。

需要输出：

1. APP 桌面图标。
2. Android Adaptive Icon 前景图。
3. Android Adaptive Icon 背景图。
4. 通知栏小图标（单色简化毛线球图案）。
5. APP 启动画面资源。
6. APP 内部品牌图标。

桌面图标可以使用完整卡通毛线球形象，通知栏小图标必须符合 Android 单色通知图标规范。

# 三、总体技术架构

采用以下架构：

业务系统 / 第三方程序  
↓  
球球推送多协议 API 网关  
↓  
统一消息处理中心  
↓  
自建 ntfy Server  
↓  
自建 Firebase 项目 / FCM  
↓  
Google Play Services  
↓  
球球推送 Android APP  
↓  
Android NotificationManager

## 3.1 技术组件

| 组件 | 推荐技术 | 职责 |
|---|---|---|
| Android 客户端 | Kotlin | FCM 接收和通知展示 |
| 推送服务器 | ntfy Server | Topic、订阅、消息缓存、FCM 集成 |
| API 兼容层 | Go | 多种 API 格式解析与转换 |
| 管理后台 | Vue 3 或轻量 Web UI | 用户、设备、密钥和消息管理 |
| 数据库 | SQLite 起步 | API Key、用户、设备映射和配置 |
| 部署 | Docker Compose | 服务端容器化部署 |
| 推送传输 | Firebase FCM | 系统级推送 |

优先采用成熟开源项目，不要重复开发已经成熟的 FCM 和 ntfy 核心功能。

## 3.2 开源项目基础

优先 Fork：

ntfy Server：
https://github.com/binwiederhier/ntfy

ntfy Android：
https://github.com/binwiederhier/ntfy-android

官方文档：
https://docs.ntfy.sh/

Android 客户端必须采用包含 Firebase 依赖的 Play flavor。

开发之前检查两个仓库的当前稳定版本和源码，记录实际 Git Commit。

不要凭空假设官方项目的类名、文件路径或构建方式。

# 四、核心功能：多协议 API 兼容

这是 V2.0 新增的重点功能。

球球推送必须提供一个统一的 API 兼容层，让其他软件无需重新设计推送逻辑，就能使用熟悉的接口格式向球球推送发送消息。

必须支持以下六种接口：

1. 方糖 / Server酱 API。
2. PushPlus API。
3. Bark API。
4. WxPusher API。
5. ntfy 原生 API。
6. 球球推送原生 API。

所有接口最终都转换为统一消息对象，再通过自建 ntfy Server 和 FCM 投递给球球推送 APP。

**重要：兼容的是 API 请求格式和调用行为，不是调用原平台的服务器。所有兼容接口都必须由球球推送自己的域名提供。**

## 4.1 方糖 / Server酱兼容

必须支持 Server酱 Turbo 和 Server酱³ 两种格式。

### A. Server酱 Turbo

原始官方格式：

```http
POST https://sctapi.ftqq.com/{SENDKEY}.send
```

球球推送兼容地址：

```http
POST https://push.example.com/sctapi/{SENDKEY}.send
```

请求示例：

```bash
curl -X POST \
  "https://push.example.com/sctapi/你的SENDKEY.send" \
  -d "title=服务器告警" \
  -d "desp=CPU温度异常"
```

支持字段：

| 字段 | 说明 |
|---|---|
| title | 消息标题 |
| desp | 消息正文，支持 Markdown |
| text | 正文或标题别名，按 Server酱 兼容规则处理 |
| short | 消息摘要 |
| tags | 消息标签 |

支持：

- GET。
- POST。
- application/json。
- application/x-www-form-urlencoded。
- SendKey 路径认证。

成功响应尽量保持 Server酱 风格：

```json
{
  "code": 0,
  "message": "成功",
  "data": {
    "id": "message_id"
  }
}
```

### B. Server酱³（SC3）

原始官方格式：

```http
POST https://{UID}.push.ft07.com/send/{SENDKEY}.send
```

球球推送兼容地址：

```http
POST https://push.example.com/sc3/{UID}/send/{SENDKEY}.send
```

请求示例：

```bash
curl -X POST \
  "https://push.example.com/sc3/123/send/你的SENDKEY.send" \
  -d "title=服务器告警" \
  -d "desp=服务器运行异常" \
  -d "tags=server|urgent"
```

支持：

- title
- desp
- text
- tags
- short
- GET / POST
- JSON / FORM

SendKey 的 SC3 用户编号必须正确解析。

### C. 方糖旧接口兼容

可选增加 PushBear 旧接口兼容路由：

```http
GET /pushbear/sub
```

参数：

- sendkey
- text
- desp

说明：PushBear 已停止服务，此接口仅用于兼容仍使用旧格式的程序，不代表原 PushBear 服务仍可用。

所有 Server酱 / 方糖接口最终都发送到球球推送 APP，不调用 Server酱 官方服务。

## 4.2 PushPlus 兼容

原始官方接口：

```http
POST https://www.pushplus.plus/send
```

球球推送兼容接口：

```http
POST https://push.example.com/send
```

请求示例：

```bash
curl -X POST \
  "https://push.example.com/send" \
  -H "Content-Type: application/json" \
  -d '{
    "token": "YOUR_LOCAL_TOKEN",
    "title": "任务完成",
    "content": "DeepSeek模型部署成功",
    "template": "markdown",
    "topic": "server"
  }'
```

支持字段：

| 字段 | 说明 |
|---|---|
| token | 球球推送 API 凭证 |
| title | 消息标题 |
| content | 消息正文 |
| topic | 目标主题或业务分组 |
| template | txt / html / markdown |
| channel | 兼容原参数，实际投递统一进入球球推送 APP |
| option | 兼容字段 |
| timestamp | 毫秒时间戳，可用于消息时效控制 |
| to | 兼容字段，可映射到本地目标用户 |
| callbackUrl | 可选发送结果回调 |
| pre | 兼容预处理字段 |

支持 GET 和 POST。

需要兼容：

- URL Query 参数。
- JSON Body。
- FORM Body。
- Token 放在 URL 路径的调用方式。

PushPlus 原有的 channel 如果表示微信、短信等其他第三方渠道，球球推送不调用这些外部渠道，而是将消息统一投递到自己的 APP。

响应格式尽量兼容：

```json
{
  "code": 200,
  "msg": "请求成功",
  "data": {
    "messageId": "message_id"
  }
}
```

## 4.3 Bark API 兼容

支持 Bark 常见 URL 格式。

原始格式：

```text
/{key}/{body}
/{key}/{title}/{body}
/{key}/{title}/{subtitle}/{body}
```

球球推送兼容格式：

```text
https://push.example.com/bark/{key}/{title}/{body}
```

另外支持：

```http
POST /push
```

JSON 示例：

```json
{
  "device_key": "YOUR_LOCAL_DEVICE_KEY",
  "title": "服务器告警",
  "subtitle": "Ubuntu",
  "body": "服务器 CPU 温度异常",
  "group": "server",
  "sound": "alarm",
  "url": "https://example.com/server",
  "level": "active",
  "badge": 1
}
```

支持字段：

- device_key
- title
- subtitle
- body
- group
- sound
- icon
- url
- badge
- level
- volume
- call

Android 映射规则：

| Bark 字段 | 球球推送处理 |
|---|---|
| title | 通知标题 |
| subtitle | 通知副标题或正文前缀 |
| body | 通知正文 |
| group | 通知分组 / 业务类型 |
| sound | 映射到本地声音配置 |
| icon | 只接受预定义本地图标别名 |
| url | 点击通知打开网页 |
| level | 映射 Android 通知重要性 |
| badge | 在系统支持的范围内处理 |
| volume | 不保证可控制系统音量 |
| call | 映射紧急通知策略，不保证实现 iOS 式持续响铃 |

必须区分 Bark 的 iOS 专属能力和 Android 实际支持的能力。

不允许为了声称兼容而伪造无法实现的功能。

## 4.4 WxPusher API 兼容

原始官方接口：

```http
POST https://wxpusher.zjiecode.com/api/send/message
```

球球推送兼容接口：

```http
POST https://push.example.com/api/send/message
```

请求示例：

```json
{
  "appToken": "YOUR_LOCAL_APP_TOKEN",
  "content": "服务器运行正常",
  "summary": "服务器状态",
  "contentType": 1,
  "topicIds": [123],
  "uids": ["UID_xxxx"],
  "url": "https://example.com",
  "verifyPayType": 0
}
```

支持字段：

| 字段 | 说明 |
|---|---|
| appToken | 球球推送本地应用凭证 |
| content | 消息内容 |
| summary | 消息摘要 |
| contentType | 1 文本、2 HTML、3 Markdown |
| topicIds | 目标主题 ID 数组 |
| uids | 目标用户 / 设备 ID 数组 |
| url | 点击跳转链接 |
| verifyPayType | 兼容字段，不涉及付费订阅校验 |

支持：

- POST JSON。
- GET Query。
- 单用户推送。
- 多用户推送。
- Topic 群发。
- HTML、Markdown 和纯文本。

原 WxPusher 的 UID、TopicId、AppToken 不会自动转换为球球推送内部 ID。

必须在管理后台提供对应的用户和设备映射机制，并为球球推送签发自己的 AppToken、UID 和 TopicId。

返回格式尽量兼容：

```json
{
  "code": 1000,
  "msg": "处理成功",
  "data": [
    {
      "uid": "UID_xxxx",
      "messageContentId": 123456,
      "code": 1000,
      "status": "创建发送任务成功"
    }
  ],
  "success": true
}
```

注意：1000 表示服务器成功受理，不代表 Android 设备已经显示通知。

## 4.5 ntfy 原生 API

必须完整保留 ntfy 的原生推送调用方式。

例如：

```bash
curl -X POST \
  "https://push.example.com/server-alert" \
  -H "Authorization: Bearer YOUR_TOKEN" \
  -H "Title: 服务器告警" \
  -H "Priority: urgent" \
  -H "Tags: warning,server" \
  -H "Click: https://example.com/server/123" \
  -d "服务器 CPU 温度异常"
```

支持：

- POST / PUT。
- Topic 路由。
- Title。
- Message。
- Priority。
- Tags。
- Markdown。
- Click。
- Actions。
- Attachments。
- 消息缓存。
- 用户认证。

尽量直接复用 ntfy 官方协议，不要为了兼容其他 API 而破坏 ntfy 原生接口。

## 4.6 球球推送原生 API

同时设计自己的标准化 API，供新项目使用。

接口：

```http
POST /api/v1/push
```

请求示例：

```json
{
  "token": "YOUR_LOCAL_API_TOKEN",
  "topic": "server-alert",
  "business_type": "server",
  "title": "服务器告警",
  "body": "CPU温度超过90度",
  "priority": 5,
  "sound": "alarm",
  "icon": "server",
  "click": "https://example.com/server/123",
  "tags": ["warning", "server"]
}
```

字段说明：

| 字段 | 说明 |
|---|---|
| token | API 凭证 |
| topic | 推送主题 |
| business_type | 业务类型 |
| title | 通知标题 |
| body | 通知正文 |
| priority | 1-5 优先级 |
| sound | 本地声音配置别名 |
| icon | 本地图标别名 |
| click | 点击跳转 URL |
| tags | 标签 |

此接口是球球推送长期推荐的标准接口。

# 五、API 兼容层架构

必须采用统一消息转换设计。

所有外部协议适配器不能各自独立实现 FCM 发送。

统一流程：

接收 HTTP 请求  
↓  
识别 API 格式  
↓  
解析参数  
↓  
验证 API Key  
↓  
解析目标用户和 Topic  
↓  
转换为统一消息对象  
↓  
调用 ntfy 原生消息发布接口  
↓  
ntfy Server 调用 Firebase FCM  
↓  
球球推送 APP 接收消息

建议服务端项目结构：

```text
qiuqiu-push/
├── server/
│   ├── api/
│   │   ├── native.go
│   │   ├── ntfy.go
│   │   ├── serverchan.go
│   │   ├── pushplus.go
│   │   ├── bark.go
│   │   └── wxpusher.go
│   ├── adapter/
│   │   ├── normalizer.go
│   │   └── response.go
│   ├── auth/
│   ├── device/
│   ├── topic/
│   ├── message/
│   ├── storage/
│   └── main.go
├── android/
├── admin/
├── deploy/
└── docs/
```

实际开发应以选定仓库为基础调整，不要求机械采用上述目录。

# 六、统一消息对象

不同 API 转换后必须生成统一消息对象。

建议结构：

```json
{
  "message_id": "uuid",
  "user_id": "user_001",
  "device_ids": [],
  "topic": "server-alert",
  "business_type": "server",
  "title": "服务器告警",
  "body": "CPU温度异常",
  "content_type": "markdown",
  "priority": 5,
  "icon": "server",
  "sound": "alarm",
  "click_url": "https://example.com",
  "tags": ["server", "warning"],
  "created_at": 1795000000,
  "source_protocol": "serverchan"
}
```

要求：

- 所有 API 统一转换。
- 统一消息 ID。
- 统一错误处理。
- 统一目标用户映射。
- 统一消息路由。
- 统一通知展示规则。
- 统一日志和统计。

必须保留 source_protocol 字段，用于判断消息来自哪种兼容接口。

# 七、多用户与 API Key 管理

球球推送服务端需要支持多个第三方程序接入。

管理后台至少提供：

1. 创建用户。
2. 创建应用。
3. 创建 API Key。
4. 创建 Server酱风格 SendKey。
5. 创建 PushPlus 风格 Token。
6. 创建 Bark 风格 Device Key。
7. 创建 WxPusher 风格 AppToken。
8. 创建 Topic。
9. 绑定用户和设备。
10. 绑定 Topic 和设备。
11. 查看发送记录。
12. 查看 API 调用次数。
13. 查看错误日志。
14. 禁用和重置密钥。
15. 发送测试通知。

不同 API Key 必须能够映射到指定用户、设备或 Topic。

禁止所有用户共用一个全局 API Key。

密钥必须随机生成、可撤销，不能明文记录到日志。

不同用户之间必须相互隔离。

# 八、Android APP 功能

## 8.1 基础界面

APP 至少包含：

- 首页：推送状态和最近消息。
- 服务器：自建服务器地址和连接测试。
- 订阅：Topic 管理。
- 通知：消息历史。
- 业务设置：图标、声音和通知渠道。
- API：查看自己的调用凭证和接口示例。
- 关于：版本和开源许可。

## 8.2 FCM

必须使用 Firebase Cloud Messaging。

使用自己的 Firebase 项目。

服务端直接通过 Firebase Admin SDK 发送 FCM 消息。

Android 客户端使用 FirebaseMessagingService 接收。

不得通过其他商业推送平台中转。

不得自行实现常驻 WebSocket 或 HTTP 轮询。

保留 ntfy 已有的 FCM 注册、订阅和消息接收机制，优先扩展现有代码。

## 8.3 不同业务不同图标

初始图标：

| 业务 | 图标 |
|---|---|
| 服务器 | ic_server |
| 安全 | ic_security |
| 邮件 | ic_email |
| 任务 | ic_task |
| 网络 | ic_network |
| 自定义 | ic_custom |

支持后续添加新业务图标。

通知图标必须通过 Android NotificationCompat 设置。

## 8.4 不同业务不同声音

使用 Android NotificationChannel。

初始渠道：

- server_alert
- security_alert
- email_notice
- task_complete
- network_alert
- custom_notice

每个业务允许独立配置：

- 声音。
- 振动。
- 通知重要性。
- 通知显示方式。

声音通过 APP 内置资源或用户选择的系统声音实现。

注意 Android 8.0 及以上通知渠道的系统管理限制，不能在每次收到通知时强制修改用户已设定的渠道声音。

## 8.5 点击通知打开网页

兼容：

- ntfy Click。
- PushPlus 相关 URL 扩展字段。
- Bark url。
- WxPusher url。
- 球球推送 click。

最终统一调用 Android ACTION_VIEW。

需要支持：

- 普通 HTTPS 链接。
- 自定义 Deep Link。
- APP 内部详情页面。

必须对 URL 做基本安全校验。

## 8.6 流体云

目标设备为 OPPO Find N5 ColorOS 16。

流体云作为专项适配功能。

必须先验证第三方 APP 通过公开通知接口能否进入流体云。

不能将普通通知等同于流体云通知。

不能依赖 Root、系统签名或修改系统 APK。

无法实现时明确记录限制，不得影响基础通知。

# 九、低功耗设计

项目必须使用 FCM 作为唯一后台推送通道。

APP 不允许：

- 常驻前台服务。
- 后台 WebSocket。
- HTTP 定时轮询。
- 永久 WakeLock。
- 定时保活。
- 无消息时定时请求服务器。
- 通过其他 APP 保活。

没有消息时，APP 不应持续产生自己的网络活动。

通知到达时通过 Google Play Services 的 FCM 回调处理。

APP 必须尽快展示通知，避免在 FCM 回调中执行耗时任务。

# 十、Firebase 和服务器部署

## 10.1 Firebase

使用自己的 Firebase 项目。

Android 文件：

```text
app/google-services.json
```

服务端 Firebase Admin SDK 凭证：

```text
firebase-adminsdk.json
```

服务端凭证只能存放在服务器安全目录中。

不得放进 APK 或 Git 仓库。

必须核对 Android applicationId 与 Firebase 注册包名一致。

## 10.2 Docker 部署

服务器：

Ubuntu 24.04。

推荐服务：

- ntfy Server。
- 球球推送 API Gateway。
- SQLite。
- 管理后台。

建议 Docker Compose 管理。

服务间通过 Docker 内部网络通信。

API Gateway 不直接实现 FCM 底层协议，而是将标准化消息提交给 ntfy Server，由 ntfy 的 Firebase 集成完成发送。

这样可以最大限度保留 ntfy 官方的推送实现。

# 十一、API 兼容性验收

必须为每一种兼容 API 建立独立测试用例。

| 协议 | GET | POST FORM | POST JSON | 多用户 | 点击链接 |
|---|---|---|---|---|---|
| Server酱 Turbo | 必须 | 必须 | 必须 | 支持映射 | 支持 |
| Server酱³ | 必须 | 必须 | 必须 | 支持映射 | 支持 |
| PushPlus | 必须 | 必须 | 必须 | 支持映射 | 支持 |
| Bark | 必须 | 必须 | 必须 | 支持映射 | 支持 |
| WxPusher | 支持 | 可选 | 必须 | 必须 | 支持 |
| ntfy | GET Webhook | 不要求 | 原生协议 | 必须 | 支持 |
| 球球推送 | 支持 | 可选 | 必须 | 必须 | 支持 |

兼容性测试必须覆盖：

1. 中文内容。
2. URL 编码。
3. Markdown。
4. HTML。
5. 空标题。
6. 超长正文。
7. 错误 Token。
8. 无权限 Topic。
9. 无效用户 ID。
10. 重复消息。
11. 多用户发送。
12. 点击跳转。
13. 不同业务声音。
14. 不同业务图标。

对于无法完全对应 Android 的 Bark / PushPlus / WxPusher 字段，必须明确说明兼容范围，不得静默伪造功能。

# 十二、API 调用兼容的边界

这里的兼容是：

- 采用原平台熟悉的 URL 路径结构。
- 采用原平台的请求字段。
- 采用原平台的 GET、POST、JSON、FORM 等请求方式。
- 尽量保持原平台的响应 JSON 结构。
- 由球球推送自己的服务端处理请求。

必须明确：

原有程序如果把官方服务器域名写死，例如：

```text
https://sctapi.ftqq.com/
```

那么仅部署兼容 API 并不能自动让它转向球球推送。

调用方必须能够修改 API Base URL，或者提供可配置的自定义 Endpoint。

不能宣称所有现有第三方软件都能在不修改任何配置的情况下直接切换。

各平台原有 SendKey、Token、Device Key、UID 也不能自动迁移，需要在球球推送后台重新生成或映射。

# 十三、开发阶段

## 第一阶段：基础推送闭环

- Fork ntfy Server。
- Fork ntfy Android。
- 配置自己的 Firebase。
- 部署自建 ntfy。
- 修改 APP 名称为球球推送。
- 设计毛线球球图标。
- 修改默认服务器地址。
- 实现 FCM 推送。
- 完成真实设备测试。

验收：自己的服务器可以直接向球球推送 APP 发送 FCM 消息。

## 第二阶段：通知个性化

- 不同业务图标。
- 不同声音。
- 通知渠道管理。
- 点击网页。
- 通知历史。
- Topic 订阅管理。

验收：不同业务可以呈现不同图标和声音。

## 第三阶段：API 兼容层

按顺序实现：

1. ntfy 原生 API。
2. 球球推送原生 API。
3. Server酱 Turbo。
4. Server酱³。
5. PushPlus。
6. Bark。
7. WxPusher。
8. 方糖旧接口兼容。

每完成一种协议，都提供 curl 测试脚本。

验收：第三方程序使用熟悉的 API 格式，能够通过球球推送自己的服务器将通知发到 APP。

## 第四阶段：多用户和管理后台

- 用户管理。
- 应用管理。
- API Key 管理。
- Topic 管理。
- 设备绑定。
- 消息记录。
- 调用统计。
- 权限控制。

验收：多个不同用户、多个第三方程序能够相互隔离地使用服务。

## 第五阶段：ColorOS 16 优化

- 锁屏通知。
- Doze。
- APP 后台划掉。
- 手机重启。
- 通知声音。
- 通知图标。
- 流体云专项测试。
- 低功耗测试。

验收：OPPO Find N5 上长期稳定接收 FCM 通知。

# 十四、最终交付要求

必须交付：

1. 球球推送 Android 完整源码。
2. 球球推送 APP Release APK。
3. 卡通毛线球球全套图标。
4. Firebase 配置指南。
5. 自建 ntfy Server Docker Compose。
6. API Gateway 完整源码。
7. 管理后台源码。
8. SQLite 数据库结构。
9. 多协议 API 接口文档。
10. 每种 API 的 curl 测试脚本。
11. Ubuntu 24.04 部署文档。
12. APP 使用说明。
13. API Key 管理说明。
14. 真实设备测试报告。

# 十五、给 AI 开发代理的执行指令

你现在是本项目的技术负责人，负责 Android、Go、Firebase、Docker 和 API 兼容层的实际开发。

不要只输出技术方案，要实际检查源码、编写代码、构建和测试。

执行顺序：

1. 先检查 ntfy Server 和 ntfy Android 当前源码。
2. 记录两个仓库的版本和 Commit。
3. 分析官方 FCM 消息发送和接收机制。
4. 设计球球推送统一消息数据结构。
5. 先完成基础 FCM 推送闭环。
6. 完成 APP 品牌、名称和毛线球球图标。
7. 实现不同业务通知图标和声音。
8. 实现点击通知打开网页。
9. 开发 Go API 兼容层。
10. 按照本规格书逐一实现 API 兼容协议。
11. 开发多用户管理后台。
12. 完成 Docker 部署和自动化测试。
13. 在 OPPO Find N5 ColorOS 16 上验证。

必须遵守：

- 不要把不同平台的 API 直接转发到原平台服务器。
- 不要依赖方糖、Server酱、PushPlus、Bark、WxPusher 的官方推送服务。
- 不要把 API 兼容理解为只做一个统一 JSON 接口。
- 不要遗漏原平台的路径、参数和请求格式兼容。
- 不要将不同用户的 API Key 混用。
- 不要将所有通知都使用同一个图标和声音。
- 不要使用后台常驻连接代替 FCM。
- 不要为了流体云功能破坏标准 Android 通知。
- 不要只提供伪代码或没有经过编译的示例。
- 每个开发阶段都必须提供实际代码和验证结果。

最终产品：

**球球推送 = 自建 FCM 推送服务端 + 多协议 API 兼容平台 + 自定义 Android 通知客户端。**

目标是让用户可以把原来使用 Server酱、PushPlus、Bark、WxPusher、ntfy 的程序，尽可能低成本地切换到自己的球球推送服务器，实现统一的自建通知服务。