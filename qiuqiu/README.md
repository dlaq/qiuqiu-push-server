# 球球推送 QiuQiu Push V2.0

自建 ntfy / FCM 推送、Android Play 客户端、多协议 API 与管理后台。

本目录包含球球推送后台源码；ntfy Fork 源码位于上一级仓库根目录。Android 客户端另见 https://github.com/dlaq/qiuqiu-push-android 。**Firebase、正式域名和 OPPO Find N5 尚未接入；当前提供的 APK 为“需要配置 Firebase”的签名 Release 构建，不能直接声称已经具备可用的云端推送。** 本地测试使用真实 ntfy 服务与独立测试数据库，未调用 Server酱、PushPlus、Bark 或 WxPusher 官方投递服务。

## 从哪里开始

1. 阅读 [部署与 Firebase 配置](docs/deployment.md)，配置两个 HTTPS 域名与自己的 Firebase 项目。
2. 按 [构建说明](docs/build.md) 编译自己的 Play Release APK，保持 `io.qiuqiu.push` 与 Firebase 包名一致。
3. 登录后台创建用户、生成一次性配对码，在 APP 中绑定设备。
4. 创建 Topic、绑定设备、签发对应协议的本地凭证，使用 [API 文档](docs/api.md) 与 `scripts/curl/` 示例发送消息。
5. 根据 [测试报告](docs/validation.md) 和 [真机验收表](docs/device-acceptance.md) 补齐云端及设备验收。

## 源码结构

- `server/`：Go Gateway、SQLite schema、权限与设备配对、六类协议、原生 ntfy 透明代理与群发、投递队列、测试。
- Android 源码：独立仓库 `dlaq/qiuqiu-push-android`。
- 仓库根目录：ntfy 服务端 Fork，增加可选 `qiuqiu` 元数据与追加式缓存迁移。
- `admin/`：Vue 3 / TypeScript，包含锁定依赖与生产构建。
- `deploy/`：Dockerfiles、Compose、双域名 Caddy、Firebase 可选覆盖文件。
- `branding/`：透明毛线球前景、自适应背景及单色小图标源文件；Android 完整资源见客户端目录。
- 构建产物自行按指南生成；Git 不跟踪 APK、二进制及私钥。

## 关键行为

Android 仅使用 FCM 作为后台消息入口；收到私有 Topic 的 `poll_request` 后进行一次带认证的 HTTP 获取，并在失败时最多有限重试。没有周期消息同步、前台保活或后台订阅连接。APP 的账户会话仅保存在内存，设备读取凭证使用 Android Keystore 加密后存储。

`push.<域名>` 提供兼容接口与后台；`ntfy.<域名>` 保留原生请求路径、缓存、Actions、附件和用户认证。Gateway 通过 ntfy 的本地管理 socket 配置真实 ACL，不持有 Firebase 服务端凭证。原生 Topic 群发使用后台签发时返回的隔离 Topic 名称，避免不同用户同名 Topic 冲突。

新建的推送密钥仅完整显示一次。重置密钥会生成新密钥并撤销旧密钥；ntfy 原生密钥按“撤销后重新签发”处理。禁止使用原平台凭证直接迁移。

## 版本与许可

上游版本及完整 Commit 见 [UPSTREAM.json](UPSTREAM.json)。本仓库 Fork 自 `binwiederhier/ntfy`，用户代码位于 `main`，保留 `upstream` 远程及上游版本信息。保留上游 LICENSE、版权与许可说明。新增 Gateway、后台和客户端修改按 Apache-2.0 提供；第三方依赖遵循各自许可。品牌图由内置 imagegen 生成，原提示词见 [品牌说明](branding/README.md)。
