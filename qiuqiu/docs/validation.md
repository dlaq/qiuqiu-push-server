# 实际验证报告

验证日期：2026-11-20（北京时间）。此报告区分代码/本地集成验证和未进行的真实 Firebase、手机验证。

## 已通过

| 项目 | 实际结果与证据 |
|---|---|
| Gateway 编译 | Go 1.26.0 编译 Linux amd64 二进制成功，`go vet ./...` 通过 |
| Gateway 测试 | `go test -race ./...` 通过；9 个测试组，包括 21 个协议矩阵子场景；覆盖编码、权限、去重、撤销、重试、幂等及调用统计 |
| ntfy 服务端 | 上游 `go test ./server` 通过，耗时约 285 秒 |
| ntfy 缓存 | 完整 `go test ./message` 通过，耗时约 336 秒；历史 schema 升级与元数据重启保留测试通过；未配置 PostgreSQL 的测试按上游规则跳过 |
| 管理后台 | `npm ci`、TypeScript 检查及 Vite 生产构建通过；浏览器实测登录、创建主题、配对码对话框，桌面与 390px 移动布局无页面横向溢出，品牌图片正常加载 |
| 本地真实 ntfy 集成 | 七种入口、两台独立设备通过；长中文正文、群发去重、私有缓存认证、跨设备隔离、原生 Actions/附件、ntfy 密钥与设备撤销均通过 |
| Docker | Gateway 与 ntfy 镜像构建成功；Compose 默认配置及 Firebase 覆盖配置解析通过；独立 Compose 栈再次通过同一端到端测试 |
| Android 构建 | Play Release 已编译并签名；`io.qiuqiu.push`，versionName 2.0.0 / versionCode 20000；签名验证通过 |
| Android 单元测试 | 3 项通过：中文与业务元数据解析、普通 ntfy 消息兼容、忽略 keepalive |
| Android 模拟器测试 | 独立 Android 35 模拟器安装、启动成功；4 项 instrumentation 测试通过：通知真实展示及业务图标/声音、保留已有渠道设置、重复消息/更新处理、URL 校验与 Keystore 加密 |
| Android 数据迁移 | 在独立模拟器导入带一条历史消息的 Room v18 数据库，实际启动迁移到 v19；历史消息保留，新增元数据列为空，未清空数据库 |
| Android Lint | Release Lint：0 errors、251 warnings。存在上游及程序化界面的提示和弃用项；不声称零警告 |
| 后台策略静态检查 | 无周期工作入队调用，无前台服务启动入口，manifest 禁用 SubscriberService/重启 Receiver，无前台服务及精确闹钟权限；模拟器检查未发现正在运行的本应用服务 |
| 备份恢复 | 三个 SQLite 文件使用在线备份副本做 integrity_check 与表记录数核对；另将 Docker Gateway/ntfy 数据卷在停止写入后打包，恢复到独立新卷及新容器，用户列表一致且原管理员密码可登录 |

原始简要报告、构建结果、签名信息及测试 XML 位于 `docs/reports/`。后台截图为 `admin-desktop.png` / `admin-mobile.png`，模拟器截图为 `android-home.png` / `android-business.png`。

## 尚未验证

- 自有 Firebase 项目、真实 FCM 发送/接收及 Google Play Services 在目标手机上的表现。测试中的 ntfy 发布成功不能证明云端到达。
- OPPO Find N5 国行 ColorOS 16 的锁屏、Doze、划掉任务、重启、声音/图标长期稳定性。
- 至少 24 小时无消息情况下的真实网络与耗电观测；静态检查和模拟器短时检查不能替代它。
- 流体云：没有把普通 Android 通知当作流体云成功；尚无公开能力的设备实测结论。
- 正式域名的 DNS、Caddy ACME 证书与公网入口；测试仅使用独立本地端口和容器，不申请示例域名证书。
- 回调端点的公网实际到达、生产负载与故障恢复时间；当前仅完成回调 URL/DNS/重定向限制的实现与安全测试。

## APK 的准确状态

交付的 `qiuqiu-push-2.0.0-setup-required.apk` 是真实签名的 Play Release 构建，**没有用户自己的 Firebase Android 配置**。首页会明确显示“Firebase 未配置”。构建时使用显式 `-PqiuqiuAllowUnconfiguredFirebase=true`；普通 Release 构建默认拒绝缺失配置。

请按 `deployment.md` 创建自己的项目，再按 `build.md` 配置并重新构建。不要把此检查构建视为真实 FCM 闭环已验收。签名私钥位于源码目录之外的保密目录，需要单独备份；未打包进源码包。

## 保留的边界

项目已具备可编译源码、可运行本地服务、兼容接口、管理后台、签名检查 APK 和复现脚本。只有补齐 Firebase、域名与目标真机并完成 `device-acceptance.md` 中的实际验收，才能标记最终产品完全验收通过。
