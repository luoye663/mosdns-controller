# 二进制部署包

GitHub Release 提供 Linux `amd64` 和 `arm64` 部署包。在使用 systemd 的目标机执行以下命令，脚本会自动识别架构，下载并校验最新的 GitHub Release 二进制包，然后安装并启动服务：

```bash
curl -fsSL https://raw.githubusercontent.com/luoye663/mosdns-controller/main/deploy/binary/install-release.sh | sudo bash
```

如需安装指定版本，先下载安装脚本，再通过 `VERSION` 指定版本号：

```bash
curl -fsSLO https://raw.githubusercontent.com/luoye663/mosdns-controller/main/deploy/binary/install-release.sh
sudo VERSION=1.2.3 bash install-release.sh
```

安装完成后访问 `http://<部署主机IP>:8080` 初始化管理员，并确认服务状态：

```bash
systemctl status mosdns.service mosdns-controller.service
curl -fsS http://127.0.0.1:8080/health/ready
```

安装包包含两个二进制、生产配置、静态规则、systemd 服务文件和安装脚本，不包含 token、SQLite 数据、缓存或规则快照。初始 `default_dns` 上游组使用 Cloudflare DoH（`https://cloudflare-dns.com/dns-query`）。首次启动后可在 WebUI 管理默认组、创建其他上游组并切换默认组；也可在启动前编辑 `/etc/mosdns-manager/mosdns/config.yaml`。

安装程序不会覆盖已有 token，但会覆盖二进制、配置、规则和 systemd 文件。升级前应备份 `/etc/mosdns-manager`、`/var/lib/mosdns` 和 `/var/lib/mosdns-controller`。需要自行构建部署包时，可执行 `make binary-package`；生成文件位于 `deploy/binary/mosdns-manager-<os>-<arch>.tar.gz`。

管理员无法登录时，按[运维手册的重置密码流程](../../docs/operations.md#重置管理员密码)执行 `controller reset-password`。该命令从标准输入读取新密码，并会撤销该管理员已有的所有 Session。
