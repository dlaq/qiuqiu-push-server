# 球球推送 2.1：接收回执、直连与通道策略

本文件记录本次新增功能。构建成功、提供方受理、APP接收、系统发布通知和用户打开是不同结果。真机及第三方生产账号验收需另行记录，不能用本地测试替代。

## 日常操作

1. 更新后台和APK，原有配对与消息数据库保留。老设备第一次上报回执时，会用已有ntfy读取凭证验证身份，换取独立设备API凭证，无需重新配对。
2. 在APP「服务器」选择FCM、后台直连或自动选择。默认FCM不建立应用长连接；直连和自动模式由用户开启，显示常驻通知、维护WebSocket、断线退避重连，并在系统允许时于重启后恢复。自动模式直连优先，不在线则使用FCM；直连发出后60秒未收到接收回执，再尝试FCM。
3. 后台「设备与订阅」修改易读设备名称。API也支持设备别名：PATCH `/api/v1/manage/devices/profile`，参数`id/name/alias`；别名在当前用户内唯一，不能扩大凭证授权范围。
4. 在「通道与切换」创建通道，再创建单通道、顺序切换或并行策略。每步回执等待默认为60秒，范围5–3600秒。编辑通道需重新填写完整配置，旧凭证不回显。禁用立即阻止后续使用。
5. 在「应用与密钥」给兼容接口凭证绑定默认策略。程序不改原有消息格式即可选择球球通道；单次请求可用`delivery_policy`覆盖。透明ntfy原生发布仍走ntfy，尚不支持用此设置改写原生发布路径。
6. 首页测试发送选择设备/Topic、策略、标题、正文、格式、业务显示名、优先级、声音、图标、链接、分组、标签和有效期。高级配置支持HTTPS附件及最多3个view/http按钮。设备与Topic展开后取并集，同一设备只投递一次。
7. 右侧「本次测试投递进度」展示本次实际消息UUID、各设备记录、通道尝试、回执及时间。SSE中断后暂时轮询，恢复后继续；浏览器会话保存最近30个测试ID。消息记录页可以展开任意历史消息的进度。

## 状态含义

| 状态 | 已确认的事实 |
| --- | --- |
| accepted / pending | 服务器已保存 / 尚待投递 |
| sending | 已开始一次实际通道调用 |
| published | 该条投递流程已完成；需结合通道记录判断结果 |
| waiting_receipt | ntfy或直连发送成功，等待APP确认 |
| received | 已认证APP报告消息已存入本地 |
| notification_submitted | APP已调用系统通知发布；不等于人已看到 |
| notification_blocked | 通知权限或渠道被系统/用户关闭 |
| notification_suppressed | 静音、最低优先级等应用设置未展示通知 |
| opened | 用户打开了该条消息 |
| receipt_timeout | 等待时间内未收到回执，终端接收未知；顺序策略会继续下一步 |
| provider_accepted | 第三方提供方返回受理成功，按配置视为可达；手机接收未知 |
| stored | 仅保存，没有发送 |
| failed | 本次通道未受理或流程没有成功通道 |

回执可在离线后补交，手机事件时间和服务器收取时间分别保存。接收回执只证明APP报告的接收结果。超时后仍可能收到迟到回执，也可能有跨通道重复提醒。旧APP没有回执时，后台不得显示“手机已收到”。

## 通道配置与能力

| 类型 | 所需配置 | 当前发送能力 / 验证边界 |
| --- | --- | --- |
| ntfy_fcm | 无额外凭证，使用已配对设备 | 完整球球通知、附件、操作、手机回执；真实FCM待手机验证 |
| app_direct | APP开启直连或自动 | 完整球球通知、附件、操作、手机回执；常驻服务需用户开启 |
| email | host/port/username/password/recipient | TLS465或STARTTLS587邮件；服务端SMTP接收不代表已读 |
| wechat_test | app_id/secret/template_id/recipient | 微信测试号模板，字段title/description/content/text |
| wecom_app | app_id(CorpID)/secret/agent_id/recipient | 企业微信应用文字 |
| wecom_bot | url | 企业微信群机器人文字 |
| feishu_app | app_id/secret/recipient_type/recipient | 飞书应用文字；需应用权限 |
| feishu_bot | url，可选secret | 飞书群机器人文字及签名 |
| dingtalk | url，可选secret | 钉钉群机器人文字及签名 |
| onebot | url/token/recipient_type/recipient，可选mode | HTTP、正向WS或反向WS的send_msg；需自行运行机器人服务 |
| qq_bot | app_id/secret/recipient_type/recipient | QQ官方机器人私聊/群主动消息；取决于官方权限、配额和OpenID |
| telegram | token/recipient | Bot API文字 |
| discord | url | Webhook文字，默认禁止自动@提及 |
| bark | url/key | Bark文字、分组、点击链接和声音别名；iOS显示由Bark决定 |
| webhook | url，可选token/headers/body_template | HTTPS JSON；模板只替换字符串，不执行代码 |
| client | secret，至少24字节 | message-pusher文档中的注册客户端WS；没有终端回执，socket写入成功只作为提供方受理 |
| store | 无 | 仅保存 |

除球球/ntfy外的文字通道不承诺自定义通知图标、Android重要性和声音。普通文字消息附带标题和点击链接；第三方客户端如何展示由提供方决定。大正文可能超过提供方限制，此时应记录发送失败并按策略尝试备用通道，不能伪造成功。多设备共用同一第三方通道时，同一消息复用该通道已经成功的受理结果。

OneBot反向WS入口为`wss://push.example.com/api/v1/onebot/reverse/{通道ID}`，用该通道token作为Bearer授权。正向HTTP填完整发送接口，正向WS填机器人WS地址。私网地址需要管理员在配置中显式设置`allow_private=true`。Webhook禁止私网和重定向；凭证加密保存。

腾讯云历史`SendCustomAlarmMsg`已被官方删除，界面列为停用，不能创建可发送通道。其他第三方通道代码有本地请求/错误响应测试，生产可用性仍取决于用户的有效凭证及权限。

## 新接口

管理接口使用登录会话Bearer；设备接口使用独立设备Bearer；发送接口使用对应类型的发送凭证，三者不能互换。

| 接口 | 用途 |
| --- | --- |
| GET/POST/PATCH/DELETE `/api/v1/manage/channels` | 列表、创建、完整替换配置、禁用；DELETE用`?id=` |
| GET/POST/PATCH/DELETE `/api/v1/manage/policies` | 列表、创建、编辑、删除策略；PATCH含id |
| POST `/api/v1/manage/keys/route` | `{id,delivery_policy}`设置凭证默认策略，空值清除 |
| GET `/api/v1/manage/messages/{UUID}` | 统一消息、设备投递、通道尝试、回执、真实事件与传输ID |
| GET `/api/v1/manage/events?after={事件ID}` | 带Bearer的SSE，支持Last-Event-ID，按用户过滤 |
| GET `/api/v1/messages/{UUID}` | 发送方查询；仅允许创建该消息的同一凭证 |
| POST `/api/v1/device/profile` | `{mode:fcm/direct/auto,app_version}` |
| POST `/api/v1/device/receipts` | 批量1–100条`{message_id,stage,event_time,reason}`，幂等；无关联消息逐条返回unknown_message，不阻塞其他回执 |
| POST `/api/v1/device/upgrade` | 旧版ntfy凭证验证并升级设备API凭证 |
| POST `/api/v1/manage/icons` | multipart file，PNG/JPEG最大900KB，转256px PNG；生成的随机URL公开可读 |
| POST `/api/v1/manage/keys/encryption` | 为Bark凭证设置AES-CBC/PKCS7或AES-GCM解密配置 |

通道和Bark密钥使用AES-GCM静态加密，密钥文件`/data/channel.key`权限0600。备份和恢复必须同时保留该文件及SQLite数据；只恢复DB无法解密。策略内容在消息受理时保留快照，之后编辑/删除策略不会改写已排队的步骤；禁用通道仍会阻止发送。崩溃发生在外部调用中间时，记录“受理结果未知”，不盲目重复调用。

## WebSocket调用

统一发送入口：`wss://push.example.com/api/v1/ws`。首帧`{"token":"对应协议的本地发送凭证"}`，成功返回authenticated。随后发送：

```json
{"id":"request-1","path":"/api/v1/push","body":{"title":"测试","body":"中文正文","business_type":"server","priority":4}}
```

`path`可替换为已兼容的HTTP发送路径，例如`/send`、`/push`、`/sctapi/{SENDKEY}.send`。服务器返回关联请求id、HTTP等价status和原协议response。native可带`idempotency_key`。客户端需在90秒内有活动，例如每30秒发送`{"event":"ping"}`。

这是球球WS扩展，不能声称其他平台拥有相同官方发送WS。ntfy原生的JSON/SSE/WS订阅及附件接口继续由ntfy子域透明提供。球球APP设备WS为`/api/v1/device/ws`，独立设备凭证首帧认证；不与发送密钥混用。message-pusher注册客户端WS为`/api/register_client/{用户名}?secret=...`，遵循原文档ping/pong；反向代理必须关闭含凭证URL的访问日志。

## 兼容覆盖与尚待完成的范围

已有GET/JSON/FORM、路径编码、中文、HTML清理、范围授权、撤销、多目标测试。新增Bark AES-CBC/GCM接收解密及`POST /push`的device_keys批量；新增WxPusher SPT凭证及simple-push/sptList/编码路径发送、GET topicId别名。批量逐项返回受理结果并去除重复凭证，不回显完整密钥；这是带安全扩展的响应，调用方需检查逐项状态。多个不同凭证指向同一设备时属于不同消息受理，不按内容去重。

本次仍不能标记为“全部公开消息能力兼容”：PushPlus开放AccessKey的消息列表/状态/删除、WxPusher二维码订阅/内容删除、SC3完整公开接收协议仍需继续核实并实现；原平台账户、支付、会员、任意脚本预处理不在范围内。Bark iOS专用铃声库、徽标、临界音量、强制响铃和剪贴板行为不能由Android等价保证。完整ntfy原生能力使用ntfy入口；球球高级操作只提供HTTPS view/http。

官方资料：[message-pusher](https://github.com/songquanpeng/message-pusher/blob/master/docs/API.md)、[Bark API](https://github.com/Finb/bark-server/blob/master/docs/API_V2.md)、[Bark加密](https://github.com/Finb/Bark/blob/master/docs/encryption.md)、[WxPusher OpenAPI](https://wxpusher.zjiecode.com/docs/openapi.yaml)、[PushPlus开放接口](https://www.pushplus.plus/doc/guide/openApi.html)、[腾讯云API删除记录](https://intl.cloud.tencent.com/document/product/248/39297)。

## 升级与验收

先备份gateway_data和ntfy_data，再更新已测试的不可变镜像版本。1Panel已有编排由1Panel更新；NPM两个域名均指向Gateway。开启WebSocket支持，SSE禁用缓冲；由于兼容接口密钥可能在路径/Query中，NPM应禁用该站点访问日志，避免记录完整URL。保留旧镜像和卷备份以便回滚，回滚数据库时同时恢复channel.key。

安装同签名2.1 APK后，依次测试FCM、直连、自动及60秒故障切换。Find N5锁屏/Doze/划掉任务/重启/通知权限、强行停止的独立边界、无消息网络观测、ColorOS流体云与真实第三方提供方结果均须实测。本地模拟请求、容器成功和APK签名都不替代这些验收。
