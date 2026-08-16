# 发版说明

本文说明 `.github/workflows/release.yml` 使用的凭据、构建变量、镜像标签和首次发布配置。发布工作流由维护者在 GitHub Actions 中手动启动；工作流先验证目标提交的 CI，再构建并上传全部产物，最后发布 GitHub Release 并创建版本标签。

## GitHub Actions 凭据

进入 GitHub 仓库的 `Settings` -> `Secrets and variables` -> `Actions` -> `New repository secret`，配置以下 repository secrets：

| Secret | 必需 | 用途 |
|---|---|---|
| `DOCKERHUB_USERNAME` | 是 | 登录 Docker Hub，当前应配置为拥有 `luoye663` 命名空间推送权限的账号 |
| `DOCKERHUB_TOKEN` | 是 | Docker Hub access token，必须具备目标仓库的 Read & Write 权限 |

当前 workflow 不读取 GitHub Actions repository variables（`vars.*`），因此 Variables 页面无需添加配置。虽然 Docker Hub 用户名通常不敏感，但现有 workflow 通过 `secrets.DOCKERHUB_USERNAME` 读取它，必须按 secret 配置。

建议在 Docker Hub 的 `Account settings` -> `Personal access tokens` 创建专用 token，不要使用账号密码。发布日志不会显示 secret 的原始值，但仍应定期轮换 token，并避免在命令、文档或仓库文件中写入凭据。

`GITHUB_TOKEN` 不需要手工创建。GitHub 会为每次 workflow 运行自动生成该令牌，工作流按 job 分配以下最小权限：

- 发布校验：`actions: read`、`contents: read`
- 二进制构建：`contents: read`
- Docker 构建：`contents: read`、`packages: write`
- GitHub Release：`contents: write`

如果组织或仓库策略禁止写入 contents/packages，需在 `Settings` -> `Actions` -> `General` 检查 Workflow permissions，并确认组织策略允许 workflow 请求上述权限。

## Registry 准备

当前工作流会推送四个镜像地址：

```text
docker.io/luoye663/mosdns-manager
docker.io/luoye663/mosdns-controller
ghcr.io/luoye663/mosdns-manager
ghcr.io/luoye663/mosdns-controller
```

首次发布前应完成：

1. 确认 Docker Hub 中两个仓库已创建，且 `DOCKERHUB_USERNAME` 对应账号具有推送权限。
2. 确认 GitHub Actions 可以创建和写入仓库 owner 下的 Packages。
3. 首次成功推送 GHCR 后，进入对应 Package settings，将 Change visibility 设置为 Public。

若要更换 Docker Hub 或 GHCR 命名空间，必须同步修改 `.github/workflows/release.yml` 中两个 `docker/metadata-action` 的 `images` 列表，以及 `deploy/docker-compose.yml` 和 README 中的镜像地址。

## 构建变量

二进制发布通过 `make binary-package` 构建。以下 Make 变量可在命令行覆盖：

| 变量 | 本地默认值 | 发布工作流中的值 | 说明 |
|---|---|---|---|
| `PROJECT_VERSION` | `git describe --tags --always --dirty`，失败时为 `dev` | 手动输入的完整版本，例如 `v1.2.3` | 显示在两个程序的版本信息中 |
| `GIT_COMMIT` | 当前短 commit，失败时为 `unknown` | 已通过 CI 的目标完整 commit | 用于定位构建源码 |
| `BUILD_TIME` | 当前 UTC 时间，失败时为 `unknown` | 目标 commit 的 ISO 8601 时间 | 使用 commit 时间可使重复构建元数据一致 |
| `MOSDNS_BASE` | `v5.3.4` | `v5.3.4` | 声明兼容的 mosdns 基础版本 |
| `BINARY_GOOS` | 当前 Go 环境的 GOOS，失败时为 `linux` | `linux` | 二进制目标操作系统 |
| `BINARY_GOARCH` | 当前 Go 环境的 GOARCH，失败时为 `amd64` | matrix 中的 `amd64` 或 `arm64` | 二进制目标架构和归档名的一部分 |

本地构建指定版本的示例：

```bash
npm --prefix web ci
make binary-package \
  PROJECT_VERSION=v1.2.3 \
  GIT_COMMIT="$(git rev-parse HEAD)" \
  BUILD_TIME="$(git show -s --format=%cI HEAD)" \
  BINARY_GOOS=linux \
  BINARY_GOARCH=amd64
```

生成文件为 `deploy/binary/mosdns-manager-linux-amd64.tar.gz`。构建使用 `CGO_ENABLED=0`、`-trimpath` 和 `-s -w`，两个二进制均注入相同的版本元数据。

上述值是 Make 变量和 Docker build args，不是运行时环境变量，也不应配置为 GitHub secrets。发布工作流会从已校验的版本输入和目标 commit 计算它们。

Docker 镜像使用各自源码的 commit：controller 镜像注入已通过 CI 的主仓库 commit，mosdns 镜像注入根仓库锁定的 `mosdns/` 子模块 commit。这样 `version` 命令可准确定位两个镜像各自的源码。

发布工作流还会渲染生产 Compose 配置，并生成 `mosdns-manager-docker.tar.gz`。归档包含 `docker-compose.yml`、`.env`、`mosdns/config.yaml`、`controller/config.yaml` 和空的 `secrets/` 目录；`.env` 中的 `MOSDNS_VERSION` 固定为当前 Release 对应的镜像标签。

## 可调整的发布参数

以下配置当前直接写在 `.github/workflows/release.yml` 中：

| 配置 | 当前值 | 修改位置 |
|---|---|---|
| 发布入口 | `workflow_dispatch`，输入严格校验的完整 SemVer | `on.workflow_dispatch` 和 `validate` job |
| 默认分支 | `main` | CI 分支过滤和 Release 校验 |
| 权威 CI | `.github/workflows/ci.yml` 的 `main` push run | `validate` job |
| 二进制架构 | `amd64`、`arm64` | `binaries.strategy.matrix.goarch` |
| 镜像架构 | `linux/amd64,linux/arm64` | 两个 `docker/build-push-action` 的 `platforms` |
| Go 版本 | `1.26.x` | `actions/setup-go` |
| Node.js 版本 | `22` | `actions/setup-node` |
| mosdns 基础版本 | `v5.3.4` | Makefile、两个 Dockerfile 和 workflow 的 `MOSDNS_BASE` |

调整 `MOSDNS_BASE` 时必须同时检查并更新 Makefile、`mosdns/Dockerfile`、`controller/Dockerfile` 和发布 workflow，随后完成两个架构的二进制及镜像测试。

`mosdns/Dockerfile` 还支持 `GOPROXY` build arg，默认为 `https://proxy.golang.org,direct`。网络无法访问默认代理时，可以本地执行：

```bash
docker build \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --tag mosdns-manager:test \
  mosdns
```

如需让 GitHub Actions 固定使用其他 Go proxy，应在 mosdns 镜像构建步骤的 `build-args` 中增加 `GOPROXY=...`。公共 workflow 不应在该值中包含代理认证凭据。

## 版本与镜像标签

允许的标签示例：

```text
v1.2.3
v1.2.3-rc.1
v1.2.3-beta.2+build.5
```

SemVer build metadata 中的 `+` 不是合法的 Docker tag 字符，`docker/metadata-action` 会清理该字符。若要求 Git 标签与镜像标签除 `v` 前缀外完全一致，发布标签不要使用 `+build` metadata。

稳定标签 `v1.2.3` 会生成镜像标签：

```text
1.2.3
latest
```

预发布标签只生成完整预发布版本，例如 `1.2.3-rc.1`，不会更新 `latest`。GitHub Release 也会自动标记为 prerelease。

完整版本标签用于生产部署，保证升级和回滚都指向确定的镜像。`latest` 仅用于快速体验，会随每次稳定版发布移动。工作流不生成 major/minor 浮动标签，避免在 `0.x` 阶段出现含义不清的 `0`、`0.0` 标签，也避免使用浮动标签的用户在未显式升级时拉取到新镜像。

不要删除并重新创建已经公开发布的版本标签。需要修复发布内容时应增加 patch 版本，例如从 `v1.2.3` 发布 `v1.2.4`。

## 执行发布

`.github/workflows/ci.yml` 会在 Pull Request 和 `main` push 时运行。Release 只接受 `main` 当前提交，并要求同一 commit SHA 已存在成功完成的 `main` push CI；分支上其他提交的成功记录不能代替该检查。

发布步骤：

1. 确认目标改动和子模块引用已经合并并推送到 `main`，等待该提交的 `CI` workflow 成功。
2. 打开 GitHub 仓库的 `Actions` -> `Release` -> `Run workflow`。
3. Branch 选择 `main`，输入完整版本，例如 `v1.2.3` 或 `v1.2.3-rc.1`，然后启动 workflow。

工作流按以下顺序执行：

1. 校验版本格式、`main` 分支、目标 SHA 的 CI 结果以及已有标签和 Release 状态。
2. 并行构建 Linux amd64/arm64 二进制包、Docker 部署包和两个多架构镜像。
3. 同时将镜像推送到 Docker Hub 和 GHCR。
4. 镜像及所有归档全部成功后生成 `SHA256SUMS`，创建或复用相同版本和 SHA 的 draft Release 并上传资产。
5. 上传成功后发布 draft；版本标签在此最终发布阶段创建，不再由开发者提前推送。

GitHub Release 与外部 registry 无法原子发布。两个 registry 的镜像可能在 GitHub Release 发布前短暂可见，但任何构建或镜像推送失败都会阻止 draft 和版本标签创建。

## 失败恢复

- 无效版本、错误分支或 CI 缺失/未完成/失败：修正输入或等待 CI 后重新启动 Release。
- runner、网络、registry、构建或资产上传的暂时错误：在原 workflow run 中选择 `Re-run failed jobs`，无需删除标签。
- 资产上传中断：Release job 会复用版本和目标 SHA 均相同的未发布 draft。
- workflow 缺陷且尚无 draft 或标签：修复并合并到 `main`，等待新 SHA 的 CI 成功后以相同版本重新启动。
- 已存在指向其他 SHA 的标签或 draft、或版本已发布：workflow 会拒绝继续。已发布版本不可覆盖、移动或删除；代码修复应发布新的 patch 版本。
- 仅在确认 draft 尚未发布且远程标签不存在时，才可删除错误 draft 后从修复后的 `main` 重新发布。

## 发布后检查

1. GitHub Release 包含 `mosdns-manager-linux-amd64.tar.gz`、`mosdns-manager-linux-arm64.tar.gz`、`mosdns-manager-docker.tar.gz` 和 `SHA256SUMS`。
2. `sha256sum --check --ignore-missing SHA256SUMS` 能验证下载的归档。
3. Docker Hub 和 GHCR 的两个镜像均包含目标版本标签及 amd64/arm64 manifest。
4. 稳定版更新 `latest`，预发布版不更新 `latest`。
5. Docker 部署包中的 `.env` 固定目标镜像版本，两个配置文件可被 Compose 正确加载。
6. 解压后的 `package/bin/mosdns version` 和 `package/bin/controller version` 显示正确版本、commit、mosdns 基础版本和构建时间。

可以使用以下命令检查远程镜像架构：

```bash
docker buildx imagetools inspect docker.io/luoye663/mosdns-manager:1.2.3
docker buildx imagetools inspect ghcr.io/luoye663/mosdns-controller:1.2.3
```
