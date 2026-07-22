# mosdns-controller

本项目基于 `mosdns` 的插件系统扩展 DNS 数据面，并提供面向局域网的 DNS 管理 Web UI，用于分流解析国内外域名。


## 功能概览

- 动态白名单、黑名单、强制 local/remote 路由，规则发布不重启 DNS 监听。
- 独立 local/remote DNS 缓存，黑名单检查位于缓存之前。
- 查询审计、SQLite 统计、SSE 实时查询流，以及每次实际采纳响应的 `upstream_tag`。
- controller 不可用时，mosdns 仍使用最近成功持久化的规则快照继续解析。
- 管理员 session、CSRF 防护、内部 Bearer Token 和非 root 容器部署。


> [!WARNING]
> 本项目包含 AI 辅助生成或修订的代码。维护者会对代码进行人工审查，但**不保证**代码不存在缺陷、漏洞、兼容性问题或合规风险。
>
> 本项目按“现状”提供，不附带任何安全承诺、生产适用性承诺或完整安全审计保证。若你计划将其用于生产环境，请自行完成代码审阅、部署测试。

## 获取源码

`mosdns/` 是 Git 子模块。首次克隆时使用 `--recurse-submodules`，可同时检出根仓库锁定的 mosdns 精确提交：

```bash
git clone --recurse-submodules https://github.com/luoye663/mosdns-controller.git
cd mosdns-controller
```

已克隆但未初始化子模块的仓库，执行：

```bash
git submodule update --init --recursive
```

更新根仓库并检出其锁定的 mosdns 版本：

```bash
git pull --ff-only
git submodule update --init --recursive
```

根仓库锁定具体的 mosdns 提交，保证构建可复现。`mosdns` 的日常开发分支为 `managed-dns`；仅在需要主动获取该分支最新代码时执行：

```bash
git submodule update --remote mosdns
```

该操作会更新根仓库记录的 mosdns 提交，提交根仓库变更前应完成相应测试并检查 `git status`。

## 注意事项

### 禁用 systemd-resolved

Ubuntu / Debian 默认启用了`systemd-resolved`,会占用 `127.0.0.53:53`,停止方法:

停止并禁用：

```bash
systemctl stop systemd-resolved
systemctl disable systemd-resolved
```

## Docker部署

### 1. 准备环境

部署主机需要 Docker Engine 和 Docker Compose v2。DNS 服务会占用宿主机的 `53/udp`、`53/tcp`，管理界面占用 `8080/tcp`；请先停止或迁移已有 DNS 服务，并确认这些端口未被占用。



### 2. 配置密钥和上游

在仓库根目录执行。共享 token 供 controller 与 mosdns 的内部 API 使用，必须保密且不能提交：

```bash
umask 077
mkdir -p deploy/secrets
chmod 0700 deploy/secrets
openssl rand -hex 32 > deploy/secrets/mosdns_control_token
# Compose 的 file secret 会保留此文件权限；两个非 root 容器均需读取它。
chmod 0444 deploy/secrets/mosdns_control_token
```

`deploy/secrets/` 保持为 `0700`，token 文件可供容器内的非 root 服务读取，但普通宿主机用户不能访问该目录。不要将 token 写入 Compose 文件或提交到 Git。

mosdns 配置由 `deploy/mosdns/config.yaml.tmpl` 统一生成。执行 `make configs` 会生成 Compose、本地和集成测试配置；`make binary-package` 会直接生成包内二进制配置。首次启动后的上游管理通过 WebUI 完成，保存会原子热加载并清空对应缓存，无需重启 mosdns。请勿提交真实私有 URL。

国内、国外、白名单和黑名单域名集合均在“规则管理”的对应 tab 中作为订阅源或手工规则发布；URL 订阅可按源配置刷新间隔，TXT 上传会作为可独立启停和删除的本地源保存。

### 3. 构建并启动

```bash
docker build \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --tag luoye663/mosdns-manager:latest \
  mosdns
docker build \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --file controller/Dockerfile \
  --tag luoye663/mosdns-controller:latest \
  .
docker compose -f deploy/docker-compose.yml up -d
docker compose -f deploy/docker-compose.yml ps
docker compose -f deploy/docker-compose.yml logs --tail=100 mosdns controller
```

状态为 `healthy` 后，访问 `http://<部署主机IP>:8080`。仅 `53/udp`、`53/tcp` 与 `8080/tcp` 映射到宿主机；mosdns API `9091` 和 controller ingest `8081` 仅在 Compose 内部网络开放。

如果需要通过 SOCKS5 代理拉取基础镜像或下载构建依赖时，可在运行 Docker 命令的终端设置：

```bash
export ALL_PROXY=socks5://127.0.0.1:10808
docker build --build-arg GOPROXY=https://goproxy.cn,direct --tag mosdns-manager-mosdns:latest mosdns
docker build --build-arg GOPROXY=https://goproxy.cn,direct --file controller/Dockerfile --tag mosdns-manager-controller:latest .
docker compose -f deploy/docker-compose.yml up -d
```

不要将代理地址或凭证写入 Dockerfile、Compose 文件或 Git。

### 4. 创建首个管理员

首次访问 `http://<部署主机IP>:8080` 时，WebUI 会显示管理员初始化页面。设置用户名和至少 8 个字符的密码后会自动登录。

也可使用 CLI 初始化。命令中的密码会出现在 shell 历史记录中，生产环境应使用受控终端并在执行后清理历史记录。

```bash
docker compose -f deploy/docker-compose.yml exec controller \
  controller create-admin \
  -config /etc/mosdns-controller/config.yaml \
  -username admin \
  -password '请替换为高强度密码'
```

管理员只能初始化一次；完成后首次初始化接口会关闭。首次使用前，应先在 WebUI 确认系统状态和当前规则版本，再让局域网客户端使用该主机作为 DNS 服务器。

### 5. 验证服务

```bash
curl -fsS http://127.0.0.1:8080/health/ready
dig @127.0.0.1 example.com A
dig @127.0.0.1 example.com A +tcp
```

如果未安装 `dig`，可使用项目自带的健康检查命令：

```bash
docker compose -f deploy/docker-compose.yml exec mosdns \
  dns-healthcheck --server 127.0.0.1:53 --network udp
```

停止服务但保留规则快照、缓存和 SQLite 数据：

```bash
docker compose -f deploy/docker-compose.yml down
```

`down -v` 会删除命名卷中的运行数据，仅应在确认不需要恢复数据时使用。

## 二进制部署

`deploy/binary/` 保存原生 systemd 部署所需的模板：服务文件、配置、安装脚本与说明。

`make binary-package` 将它们与当前平台的 `mosdns`、嵌入 WebUI 的 `controller` 二进制一起生成至 `deploy/binary/package/`，同时创建可传输的 `.tar.gz`；生成物被 Git 忽略，不含 token、SQLite 数据、缓存或规则快照。

```bash
npm --prefix web ci
make binary-package
# 交叉编译示例：make binary-package BINARY_GOOS=linux BINARY_GOARCH=arm64
make binary-copy BINARY_HOST=root@dns-host
```

在目标机进入复制后的目录并安装：

```bash
sudo ./install.sh
sudo systemctl start mosdns.service mosdns-controller.service
```

安装脚本创建最小权限服务用户、运行数据目录和共享 token，并将 [mosdns.service](deploy/binary/mosdns.service) 与 [mosdns-controller.service](deploy/binary/mosdns-controller.service) 安装到 `/etc/systemd/system/`。远程 DoH 默认使用 Cloudflare（`https://cloudflare-dns.com/dns-query`）；如需其他上游，可在启动前编辑 `/etc/mosdns-manager/mosdns/config.yaml`。DNS `53/tcp`、`53/udp` 与 WebUI `8080/tcp` 对外监听，mosdns API `9091` 和 controller ingest `8081` 仅监听 `127.0.0.1`。详细目录结构与升级说明见 [deploy/binary/README.md](deploy/binary/README.md)。

首次启动后，访问 `http://<部署主机IP>:8080` 初始化管理员，并使用 `curl -fsS http://127.0.0.1:8080/health/ready`、`dig @127.0.0.1 example.com A` 与 `dig @127.0.0.1 example.com A +tcp` 验证服务。升级前备份 `/etc/mosdns-manager`、`/var/lib/mosdns` 和 `/var/lib/mosdns-controller`；详见 [升级说明](docs/upgrade.md) 与 [恢复说明](docs/recovery.md)。

查看二进制部署的服务日志：

```bash
# 实时跟随两个服务
sudo journalctl -fu mosdns.service -u mosdns-controller.service
# 查看本次启动后的日志
sudo journalctl -b -u mosdns.service -u mosdns-controller.service
# 仅查看近期错误
sudo journalctl -p err..alert -u mosdns.service -u mosdns-controller.service
```

## 本地编译与测试

开发环境需要 Go `1.26.5` 或兼容版本、Node.js 22、npm 和 Docker Compose。mosdns 的 `go.mod` 基线为 Go `1.24.9`，controller 为 Go `1.25.0`。

安装 WebUI 依赖后，可执行完整的本地构建检查：

```bash
npm --prefix web ci
make test
make build
make race
make lint
```

常用目标：

| 命令 | 用途 |
|---|---|
| `make test` | Go 单元测试与 WebUI 类型检查 |
| `make build` | 编译 mosdns、controller，并构建 WebUI 静态资源 |
| `make race` | dynamic_rule_engine、query_audit 和 controller 的 race 检查 |
| `make lint` | Go vet 与 WebUI 类型检查 |
| `make web-embed` | 构建 WebUI 并同步到 controller 的 `go:embed` 静态目录 |
| `make compose-up` | 前台构建并启动完整 Compose 环境 |
| `make compose-down` | 停止 Compose 环境并保留命名卷 |

如需得到本机可直接执行的二进制文件：

```bash
mkdir -p bin
go -C mosdns build -o ../bin/mosdns .
go -C controller build -o ../bin/controller ./cmd/controller
```

### 本地运行完整服务(开发时)

`deploy/local/` 提供不依赖 Docker 的开发配置。它只监听本机回环地址，端口为 DNS `5353`、WebUI/API `18080`、内部 ingest `18081`、mosdns API `19091`，所有本地数据均写入被 Git 忽略的 `.local/`。配置中的公开 DoH 仅用于开发功能验证，不应用于生产。

先执行初始化，然后在两个终端分别运行以下命令：

```bash
make local-init
make local-mosdns
```

```bash
make local-controller
```

也可使用 `make local-up` 同时启动两项服务。服务启动后访问 `http://127.0.0.1:18080` 创建首个管理员；也可以在第三个终端运行 `make local-create-admin`。本地 DNS 验证命令为 `dig @127.0.0.1 -p 5353 example.com A`。

`make local-clean` 仅删除 `.local/` 下的本机开发数据库、缓存、快照和 token，不会影响 Docker Compose 的命名卷。WebUI 的生产静态资源会在 `controller/Dockerfile` 构建镜像时嵌入 controller；执行 `npm --prefix web run dev` 后，Vite 会将 `/api` 请求反代到本地 controller `http://127.0.0.1:18080`。可通过环境变量覆盖后端地址，例如 `VITE_API_PROXY_TARGET=http://127.0.0.1:8080 npm --prefix web run dev`。

## 集成环境验证

集成 Compose 会启动两个本地 CoreDNS mock upstream，并仅开放 `5353/udp` 和 `5353/tcp`。它不需要真实远程 DoH，适合在迁移 `53` 前验证 DNS 路径：

```bash
umask 077
mkdir -p deploy/secrets
chmod 0700 deploy/secrets
openssl rand -hex 32 > deploy/secrets/mosdns_control_token
chmod 0444 deploy/secrets/mosdns_control_token
make configs
docker compose -f deploy/docker-compose.integration.yml --profile integration up --build
```

另一个不依赖 Docker 的真实 mosdns 集成测试入口：

```bash
go -C mosdns test -v ./tests/integration
```

更多测试范围和清理方式见 [tests/integration/README.md](tests/integration/README.md)。

## 配置、数据与运维

- 生产 Compose：`deploy/docker-compose.yml`。
- mosdns 配置模板：`deploy/mosdns/config.yaml.tmpl`；运行配置由 `make configs` 生成。
- controller 配置模板：`deploy/controller/config.yaml.tmpl`；运行配置由 `make configs` 生成。
- 持久化数据：Compose 命名卷 `mosdns-state`（快照和缓存）与 `controller-state`（SQLite）。
- 密钥目录：`deploy/secrets/`，只保留 `.gitkeep`，实际 token 被 Git 忽略。
- 运维、备份恢复、升级与故障诊断分别见 [operations](docs/operations.md)、[recovery](docs/recovery.md)、[upgrade](docs/upgrade.md) 与 [troubleshooting](docs/troubleshooting.md)。

## 版本与许可证

- mosdns 基线：`v5.3.4`，commit `b7323188bab1ea742538aeccb31b692bc4967d1b`。
- 兼容性审计：[docs/mosdns-v5.3.4-compatibility.md](docs/mosdns-v5.3.4-compatibility.md)。
- 开发说明：[docs/development.md](docs/development.md)。
- GPL 边界：[docs/license-boundary.md](docs/license-boundary.md)。
- 不得提交 token、密码、真实私有 DoH URL、生成的 secret 文件或 DNS 数据。
