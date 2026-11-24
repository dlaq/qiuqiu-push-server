# Ubuntu 24.04 部署与 Firebase 配置

## 部署资源

需要一台能访问 Google Firebase 的 Ubuntu 24.04 服务器、Docker Engine 与 Compose 插件、两个指向服务器的域名（示例 `push.example.com`、`ntfy.example.com`），以及手机上可用的 Google Play Services 和 FCM 网络。

开放服务器 TCP 80/443。Caddy 自动申请 HTTPS 证书，Gateway 和 ntfy 不直接暴露容器端口。客户端只接受 HTTPS 服务器根地址。本地集成测试专用的 HTTP 入口不能当成手机生产配置。

## 首次部署

```sh
cd qiuqiu-push-server/qiuqiu
cp deploy/.env.example deploy/.env
# 编辑 deploy/.env 中两个域名
cd deploy
docker compose build
cd ..
python3 scripts/init-secrets.py
cd deploy
docker compose up -d
```

`init-secrets.py` 在本机终端交互读取后台管理员密码，不打印密码。`deploy/secrets/` 为 0700，文件为 0600，不入 Git。脚本使用已构建的 Gateway `hash-password` 子命令或 python3-bcrypt 计算 ntfy 的 bcrypt 哈希。

后台初始用户名 `admin`，密码为刚设置的密码。用户名也可通过 `QQ_ADMIN_NAME` 配置；修改初始化密码文件不会自动重置已有数据库账户。请保存密码；当前版本不提供邮箱找回。

数据库和附件使用独立 Docker 命名卷。SQLite 是嵌入式数据库，不是另一个数据库容器。

## 自己的 Firebase 项目

1. 在 Firebase 控制台创建项目，并注册 Android 应用 **`io.qiuqiu.push`**。
2. 下载 `google-services.json` 到 独立 Android 仓库的 `app/google-services.json`。该配置含项目标识，按本项目策略不放 Git。它不是 Firebase 服务端私钥。
3. 在 Firebase 项目设置的服务账号页面生成服务端私钥，保存在服务器的 `deploy/secrets/firebase-adminsdk.json`，权限 0600。
4. 确认私钥的 `project_id` 与 Android 配置中的项目一致，且启用 Firebase Cloud Messaging API、服务器能正常访问 Google 服务。
5. 启用 Firebase 覆盖文件：

```sh
cd deploy
docker compose -f compose.yml -f compose.firebase.yml up -d --build
```

只有 ntfy 容器读取 Firebase 私钥。Gateway 和 APK 不得包含该私钥。后续使用相同两份 Compose 文件升级。

6. 按 [构建说明](build.md) 生成正式 Play Release APK。默认没有有效 Firebase 配置时，Release 构建会失败；交付的 `setup-required.apk` 使用显式开关绕过此检查，供查看界面，不能替代自己的 Firebase 配置。

Debug applicationId 为 `io.qiuqiu.push.debug`；如果要在 Debug 构建测试 FCM，需要在同一 Firebase 项目额外注册该包名，并下载包含匹配配置的文件。

## 用户、设备与首次通知

1. 用管理员登录 `https://push.<域名>`，创建普通用户；不开放公开注册。
2. 普通用户登录后台，生成一次性配对码，有效期 10 分钟。
3. 手机 APP → 服务器，保存 `https://push.<域名>` 并测试连接，输入配对码和设备名。
4. 配对会在 ntfy 创建设备独立账户，授予仅自己收件箱的读取权限，返回设备读取 Token。Token 在手机上以 Keystore 密钥加密存储。
5. 后台发送测试通知；查看服务器记录，再在手机核对实际接收。服务端 `published` 仅代表 ntfy 接受发布，不是 FCM 或设备显示回执。
6. 创建 Topic、绑定设备，为业务程序签发独立密钥；一个用户的本地 Topic 名称与另一个用户互不共享。

手机 APP 内的账户登录用于订阅、密钥管理，会话仅保存在内存。重新启动后需再次登录管理账户；已经配对的设备推送不依赖该管理会话。

## 备份、恢复与升级

```sh
scripts/backup.sh /absolute/path/to/private-backup
```

脚本停止 Gateway/ntfy 写入后备份两个数据卷，退出时重新启动。备份包含消息和认证数据库，应限制访问。`deploy/.env`、部署秘密和签名私钥另行保密备份；不要将这些秘密与公开源码压缩包混合。

恢复到隔离环境时，先停止 Gateway/ntfy，将 `gateway_data.tar.gz`、`ntfy_data.tar.gz` 分别解压到同名数据卷，然后使用原来的部署秘密启动。恢复之后确认用户、密钥、Topic 和消息记录均存在，再恢复外部流量。

升级先备份，随后 `docker compose build && docker compose up -d`。ntfy 缓存从 v17 追加 `qiuqiu` 列到 v18；Android Room 从 v18 追加元数据列到 v19。不要对现有真实用户数据库执行清空或破坏性迁移。回滚应同时保存当前卷并在隔离环境验证旧版本能读新 schema，必要时从升级前备份恢复。

## 配置及观察

- Gateway `/healthz` 检查本地数据库；不能据此断言 Firebase 正常。
- `QQ_CALLBACK_ALLOWLIST` 默认为空；需要回调时填写精确的 HTTPS URL，以英文逗号分隔。
- 管理后台有请求计数、受理/发布/失败状态和投递次数。统计的调用数为已识别有效密钥的请求；非法凭证及解析前失败的请求不能归属给用户。
- 不启用 HTTP 原始访问日志；不要自行配置包含 URL 密钥或完整 Query 的反向代理访问日志。
- ntfy 原生 API 必须通过 ntfy 子域名进入 Gateway 的透明代理，否则不会记录原生群发和设备收件箱映射。

恢复脚本（**替换目标部署的数据卷**，仅对已备份并明确选择的目标执行）：

```sh
scripts/restore.sh /absolute/path/to/private-backup CONFIRM_REPLACE
```

脚本使用固定的 `qiuqiu-push` 项目卷名；如果自行改过 Compose 项目名，必须先按实际卷名调整脚本。测试环境使用独立 `qiuqiu-integration` 项目，不与正式卷混用。

## 2.1通道与回执升级

使用1Panel+Nginx Proxy Manager的已有部署，采用`deploy/compose.1panel.yml`；两个域名都代理Gateway的`127.0.0.1:18081`，不需要Caddy。请开启NPM WebSocket支持、禁用SSE缓冲，并防止含密钥路径/Query的访问日志。升级前保留完整数据卷备份，包括新增的`channel.key`。配置与操作见[2.1升级说明](upgrade-2.1.md)，验证结果见[2.1本地报告](reports/2026-11-24-upgrade-2.1.md)。
