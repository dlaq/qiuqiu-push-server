# 构建与源码维护

## 基线

见 `UPSTREAM.json`。两个上游源码仓库均使用本地 `qiuqiu-v2` 分支保留修改，没有替换成猜测的类或文件。两个 GitHub Fork 分别提供完整源码；上游差异保留在 Git 提交历史。

## Go Gateway

安装 Go 1.26.0，然后：

```sh
cd server
go test -race ./...
go vet ./...
go build -trimpath -o ../releases/qiuqiu-gateway-linux-amd64 .
```

首次启动必须设置 `QQ_ADMIN_PASSWORD_FILE`；Gateway 通过 `QQ_NTFY_URL` 访问 ntfy，通过 `QQ_NTFY_ADMIN_SOCKET` 使用仅内网共享的本地管理 socket，通过 `QQ_NTFY_ADMIN_PASSWORD_FILE` 读取发布账户密码。没有 Firebase 服务端 SDK 或第三方平台转发逻辑。

## 管理后台

Node 22：

```sh
cd admin
npm ci
npm run build
```

产物 `admin/dist` 由 Gateway 的 `QQ_ADMIN_DIR` 指向。开发服务器 `npm run dev` 的 `/api` 代理到本机 18080。生产环境不依赖 Node 进程。

## ntfy Fork

使用 Dockerfile 可以同时构建 ntfy 原生 Web UI 和官方文档。直接在本地构建时先按上游 Makefile 的 `web` 和 `docs` 目标生成 `server/site`、`server/docs`，然后：

```sh
cd ../ # 从 qiuqiu/ 进入 ntfy 仓库根目录
go test ./message -run 'QiuQiu|Migration'
go test ./server
go build -trimpath -o qiuqiu/releases/ntfy-qiuqiu-linux-amd64 .
```

首次下载 Go 模块、文档依赖和 Web 依赖需联网。PostgreSQL 测试需要独立的 `NTFY_TEST_DATABASE_URL`，当前部署只承诺 SQLite；没有配置时上游 PostgreSQL 测试会跳过。

## Android Play Release

客户端源码和完整构建说明位于 [dlaq/qiuqiu-push-android](https://github.com/dlaq/qiuqiu-push-android)。请在客户端仓库按 README 设置自己的 Firebase Android 配置和签名文件。后台仓库不包含 Android 私钥、密码、客户端源码副本或 APK。

当前 `qiuqiu/` 相对于根目录独立维护 Gateway、Vue 后台和 Compose；ntfy 编译在仓库根目录执行。

## 实施验证与复现

`scripts/integration-test.py` 使用真实运行的 Gateway + ntfy，创建独立测试用户和两台测试设备，覆盖七种入口、认证缓存读取、群发、附件、Actions、长中文正文和凭证撤销。它不模拟 Firebase 云端接收，也不能代替真机测试。仅在隔离测试环境运行。

```sh
QQ_TEST_URL=http://127.0.0.1:18080 \
QQ_TEST_ADMIN_PASSWORD_FILE=/private/path/test-admin-password \
python3 scripts/integration-test.py
```

Go、npm 和 Gradle 缓存、SDK、测试数据库和本地秘密不包含在源码包中。
