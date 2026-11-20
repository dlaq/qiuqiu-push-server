# 实现要点与当前边界

## 消息流

兼容请求 → 参数解析 → 对应类型 API Key → 用户/设备/Topic 授权 → SQLite 消息及逐设备投递事务 → 有界投递队列 → ntfy JSON 发布 → 自有 Firebase 项目 → Play 客户端 `FirebaseService` → 一次性认证获取 → Room → `NotificationManager`。

ntfy 仍负责实际 Firebase Admin SDK 发送。私有 Topic 的 FCM 中不放正文，客户端收到事件后调用 ntfy 原生缓存读取接口。事件获取使用唯一工作链，避免同一 Topic 的并发消息互相取消；高优先级 FCM 可请求 expedited work，额度不足回退普通一次性任务。每次请求有 HTTP 超时，最多有限失败重试。

Gateway 消息队列存储在 SQLite。进程重启恢复发送中的任务，最多三次尝试。服务器成功提交、尚未记录上游响应时崩溃可能再次发布，因此采用 at-least-once 语义；客户端按消息及元数据去重，不声称跨 ntfy/SQLite 的精确一次事务。

## 授权与存储

用户、会话、应用、设备、配对码、Topic、绑定、分协议密钥、消息、投递、原生发布记录、审计与调用计数均在 `server/schema.sql`。密码采用 bcrypt，随机令牌使用强随机值及 SHA256 验证摘要。配对码十分钟有效且只能使用一次；撤销设备会删除 ntfy 对应真实账户。

每个设备一个独立收件箱和只读 Token；Topic 与设备都绑定用户。原生推送 Token 是独立 ntfy 账户的真实 Token，只有指定 Topic/收件箱的写权限。不同协议类型不能混用密钥。Gateway 内网发布账户不向任何 APP 或调用方暴露。

`qiuqiu` 元数据为 ntfy 消息可选扩展，普通 ntfy 客户端可忽略。缓存 schema v18 追加列，历史消息为空值；Android Room v19 同样追加可空列并保留已有消息。移除了客户端的破坏性数据库回退。

## 通知与低功耗

六个业务默认渠道，另有有限的声音/重要性变体。渠道已存在时不重新创建、不覆盖系统声音/振动；显式 sound 别名选择对应变体。priority 仍映射通知重要性，但最终由系统设置控制。没有强制系统音量、绕过勿扰、持续警报或 iOS 专属行为。

上游周期轮询的调用和实现入口已移除；前台 SubscriberService 及其重启 Receiver 在 manifest 禁用，ServiceManager 只执行停止操作；不订阅 `~control`，忽略 keepalive。没有新增周期网络工作。Firebase/WorkManager 自身短暂唤醒和由消息触发的必要网络开销仍属正常系统行为。

## 当前功能限制

- 交付 APK 缺少用户 Firebase 配置，必须按指南重新构建后才能测试真实 FCM。
- 未完成 Find N5/ColorOS 16、24 小时空闲耗电、流体云验证。
- PushPlus 回调只执行一次有界尝试，不保证送达回调或设备显示回执。
- 对调用成功的统计限于可归属的有效密钥请求；无效凭证不能归属具体用户。
- 原生 ntfy 群发支持发布、标准字段、Actions、附件引用和序列更新；手工用 ntfy CLI 创建的账户不会自动变成球球用户。原生 Topic 的删除/清除事件目前按 ntfy 原生接口处理，不额外扩散成兼容层的设备清除操作。
- Android 首版一个安装绑定一个服务器/设备身份；支持管理业务 Topic，不支持在不撤销旧身份的情况下覆盖配对。
- 服务器密码找回、公开注册、跨节点集群、iOS 客户端不在当前实现中。
