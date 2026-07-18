# 二进制部署包

`make binary-package` 会生成 `deploy/binary/package/` 和同目录的 `mosdns-manager-<os>-<arch>.tar.gz` 文件。包内包含两个二进制、生产配置、静态规则、systemd 服务文件和安装脚本；不包含 token、SQLite 数据、缓存或规则快照。

在构建机执行：

```bash
make binary-package
make binary-copy BINARY_HOST=root@dns-host
```

目标机在复制目录内执行：

```bash
sudo ./install.sh
sudo systemctl start mosdns.service mosdns-controller.service
```

默认远程 DoH 上游为 Cloudflare（`https://cloudflare-dns.com/dns-query`）。如需使用其他上游，可在启动前编辑 `/etc/mosdns-manager/mosdns/config.yaml`。`install.sh` 不会覆盖已有 token，但会覆盖二进制、配置、规则和 systemd 文件；升级前应备份 `/etc/mosdns-manager`、`/var/lib/mosdns` 和 `/var/lib/mosdns-controller`。

管理员无法登录时，按[运维手册的重置密码流程](../../docs/operations.md#重置管理员密码)执行 `controller reset-password`。该命令从标准输入读取新密码，并会撤销该管理员已有的所有 Session。
