# 自建主控构建与发布

主控代码完整合入上游开发分支 `main@7d692d2`，自建版本 `1.5.5`，
接受上游功能删除并保留旧客户端协议兼容。默认分支 `owned`；
本人仓库 `main` 为相同安全工作流副本，上游历史仅通过 `upstream` 远程参考。

## 来源与构建

- 后端：当前 `R1ddle1337/komari` 的已审核 `owned` commit。
- 前端：`build-frontend/action.yml` 中固定的 `R1ddle1337/komari-web` 40 位 commit。
- 前端使用 Node `22.23.2`，只执行 `npm ci`，不更新锁文件、不读取浮动分支。
- 后端使用 Go `1.26.8`、`GOTOOLCHAIN=local`、`-mod=readonly`、`go mod verify`。
- CGO 使用 Zig `0.14.1`，下载后验证固定官方 SHA-256，不复用可污染的工具链缓存。
- 所有外部 Actions 固定完整 commit；Alpine、BuildKit、QEMU/binfmt 固定 digest。
- Docker 的 ca-certificates、curl、tzdata 保留，通过 Alpine 官方签名软件源安装。

保留 8 个发布平台：Linux amd64/arm64/386/riscv64/loong64，
Windows amd64/arm64/386。Linux 用 musl 静态链接，Windows 用 windows-gnu。
前端构建一次，将同一产物用于测试和所有平台的嵌入式资源。

## 测试与发布

`build.yml` 在 `owned` push / pull_request 时执行只读 CI，不发布。
`release.yml` 和 `snapshot.yml` 仅手动启动，且必须选择 `owned`：

```sh
gh workflow run release.yml --repo R1ddle1337/komari --ref owned -f version=1.5.1
gh workflow run snapshot.yml --repo R1ddle1337/komari --ref owned
```

流程：验证 owned 当前 commit → 构建固定前端 → 独立测试 →
全部 CGO 平台构建 → 16 个 binary/checksum 文件全部验通过 →
创建草稿并上传 → 正式发布 → 用同一轮 Linux 二进制构建镜像。

测试先用 `go test -exec /bin/true -run '^$' ./...` 预编译，不执行测试代码，
避免两个 public 包的测试二进制同名问题；随后
`timeout 60s go test -count=1 -timeout 60s ./...` 执行完整测试。
外部 GeoIP 服务和数据库下载检查需显式设置 `KOMARI_NETWORK_TESTS=1`，
发布测试默认不依赖这些外部站点；IPv6 回环测试只在环境支持时运行。
没有服务部署步骤，没有 PR 自动合并，也不会删除 release、tag 或镜像。

稳定版使用无 v 前缀的 SemVer，例如 `1.5.1`。快照采用
`Snapshot-yymmddhhMMSS-runID-attempt`，标记 prerelease、不设 latest。
版本不可重用；中途失败可能保留草稿，须检查后选择新版本。
发布 release 和镜像前都会重新检查 owned 分支头，拒绝陈旧提交继续发布。

默认任务只有 `contents: read`；写权限仅在相应发布 job 上授予。
发布输入通过环境变量传给 shell，先验证格式，不直接插入执行脚本。

## 镜像与迁移边界

镜像地址 `ghcr.io/r1ddle1337/komari`，提供 linux/amd64 和 linux/arm64。
稳定发布带版本和 latest 标签；快照带唯一版本和 snapshot 标签。
生产部署建议固定版本及 digest，保留现有数据挂载、数据库和启动参数。

本工作流不会操作生产数据库，不会修改服务器上的 Compose、备份或更新脚本。
从官方镜像迁移时，需要另外备份 SQLite 数据目录和 PostgreSQL 逻辑导出。

SHA-256 校验用于防损坏和产物一致性，不是独立数字签名；本人 GitHub 账号、
构建权限、npm/Go 依赖、官方工具链及 Alpine 软件源仍属于信任边界。

## 静态资源与升级缓存

1.5.3 为后台资源使用 `/themes/default/dist/assets/` 独立路径；HTML、Service Worker 和不存在的资源禁止 CDN 缓存。带内容哈希的资源可长期缓存。根 Service Worker 由后端维护，只迁移旧 Workbox 预缓存，不拦截请求、不预下载完整前端。前端登录请求有 12 秒超时和重试入口。

1.5.4 将懒加载预加载 URL 与入口资源使用同一独立路径，避免浏览器为同一模块请求两套 URL。

1.5.5 将管理页读取和历史图表查询分离到 HTTP，避免实时 WebSocket 排队；合并共享 UI 依赖减少首屏请求，默认首装 Agent 固定到不写 MOTD 的 1.5.15。
