# Docker 镜像发布与 ARM 部署

后台仓库的 **QiuQiu Docker** Actions 工作流使用现有的 `DOCKERHUB_USERNAME` 和 `DOCKERHUB_TOKEN` Repository Secrets 登录 Docker Hub。无需把 Docker 密码或 Firebase 私钥提交到仓库。

发布两个镜像，`<DockerHub用户名>` 取上述账号：

| 镜像 | 内容 | 平台 |
|---|---|---|
| `<DockerHub用户名>/qiuqiu-push-gateway` | Go Gateway、SQLite 与 Vue 管理后台 | linux/amd64、linux/arm64 |
| `<DockerHub用户名>/qiuqiu-push-ntfy` | 带球球元数据支持的 ntfy | linux/amd64、linux/arm64 |

ARM 指 64 位 ARM（aarch64），暂不发布 32 位 arm/v7。工作流分别使用 GitHub 原生 x86 和 ARM runner 构建，启动实际容器并验证七个入口、两台设备、私有缓存、Actions/附件及撤销，然后推送各架构镜像。两种架构均通过后才合并多平台 manifest。`latest` 与完整提交标签 `sha-<40位提交>` 指向相同 manifest；发布 `qiuqiu-vX.Y.Z` Git 标签还会增加 `X.Y.Z` 镜像标签。Actions 的 `docker-publication` 制品记录公开镜像名、digest、平台和提交。

main 的相关源码变更自动发布，也可在 Actions 页面选择 main 手动运行。沿用的上游构建、测试工作流继续运行；上游文档站与上游 Release 发布仅允许在原仓库执行，避免 Fork 误向原项目目的地发布。新工作流固定各 Action 的 commit，只有镜像登录步骤接收 Docker Token；构建阶段不接收 Firebase 或签名私钥。

## 使用已发布镜像

克隆后台仓库，进入 `qiuqiu/deploy`，按 [部署说明](deployment.md) 准备域名、`.env`、密码文件和 Firebase 凭证。还需在 `.env` 增加：

```dotenv
DOCKERHUB_NAMESPACE=<DockerHub用户名>
QQ_IMAGE_TAG=latest
```

```sh
docker compose -f compose.yml -f compose.images.yml -f compose.firebase.yml pull
docker compose -f compose.yml -f compose.images.yml -f compose.firebase.yml up -d --no-build
```

尚未配置 Firebase 时省略 `-f compose.firebase.yml`。原来的源码构建部署方式仍可使用。Docker 自动选择服务器的 amd64 或 arm64 镜像；不要强制写死 `platform: linux/amd64`。

升级前备份 SQLite 和数据卷。需要固定或回退版本时，将 `QQ_IMAGE_TAG` 设为之前已验证的 `sha-<提交>` 或版本号，然后使用同一组 Compose 文件 pull / up。镜像不包含部署密码、Firebase 私钥、运行数据库或用户消息；它们继续通过 secret 和持久卷提供。

本地复核公开镜像的架构与 digest：

```sh
docker buildx imagetools inspect <DockerHub用户名>/qiuqiu-push-gateway:latest
docker buildx imagetools inspect <DockerHub用户名>/qiuqiu-push-ntfy:latest
```

原生 runner 和多平台 manifest 的依据见 [Docker 多平台构建说明](https://docs.docker.com/build/ci/github-actions/multi-platform/) 与 [GitHub runner 参考](https://docs.github.com/en/actions/reference/runners/github-hosted-runners)。镜像集成测试不代表真实 FCM 到手机验收通过。
