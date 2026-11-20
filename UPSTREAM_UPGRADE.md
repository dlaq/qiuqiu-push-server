# 跟随 ntfy 上游升级

本仓库是真实 GitHub Fork：`dlaq/qiuqiu-push-server` → `binwiederhier/ntfy`。

- `main`：球球推送后台。ntfy 修改位于原目录，Go Gateway/Vue 后台/部署文档集中在 `qiuqiu/`，减少与上游目录冲突。
- 当前 ntfy 功能基线：稳定版本 v2.38.1；完整基线及原开发提交见 `qiuqiu/UPSTREAM.json`。
- `upstream-main`：初次发布时上游 main 的快照。它比稳定发布版新，不表示这些变化已纳入当前经过验证的产品。
- `origin` 指向自己的 Fork；`upstream` 指向 `binwiederhier/ntfy`。

## 克隆及设置远程

```sh
git clone git@github.com:dlaq/qiuqiu-push-server.git
cd qiuqiu-push-server
git remote add upstream https://github.com/binwiederhier/ntfy.git
git fetch upstream --tags
```

开发机当前仓库已配置这两个远程，不用重复添加。

## 升级流程

1. `git fetch upstream --tags`，阅读 release notes，选择实际存在的稳定版本标签。
2. 从自己的 main 创建分支：`git switch main && git switch -c upgrade/ntfy-VERSION`。
3. `git merge --no-ff 上游版本标签`，解决冲突并保留双方实际代码，不以 `-s ours` 伪造合并。
4. 重点检查 `qiuqiu` 元数据的发布/缓存/读取/FCM、SQLite 追加式迁移、私有 Topic 权限、管理 socket 参数和关闭周期 keepalive 的行为。
5. 在 `qiuqiu/` 下运行 Gateway 测试、后台构建、Compose 校验和真实 ntfy 集成；另外运行 ntfy server/message 回归。必要时同步 Android 元数据与协议。
6. 先备份测试/生产卷，验证升级和恢复；将升级分支推到自己的 origin，通过 PR 审核后合入自己的 main。

入口：`qiuqiu/README.md`、`qiuqiu/docs/deployment.md`、`qiuqiu/docs/build.md`。Firebase 私钥、真实秘密、数据库和构建二进制不入 Git。

不会自动把代码推向上游，也没有添加自动合并或定时升级；每次升级都保留评估和测试。
