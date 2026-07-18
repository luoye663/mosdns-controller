# Phase 5 DNS 集成测试

测试使用当前源码构建真实 `mosdns`，并启动两个本地 UDP mock upstream。不会访问真实 DoH，也不需要 Docker。

```bash
go -C mosdns test -v ./tests/integration
```

覆盖 UDP/TCP 查询、cache 后新增 block、allow 覆盖 block、local/remote 强制路由、路由切换时的独立缓存、受 Bearer Token 保护的 cache flush、controller 不可达，以及 1000 次快照原子发布期间的连续查询。

本地 listener 可用性探测：

```bash
go -C mosdns run ./cmd/dns-healthcheck --server 127.0.0.1:5353 --network udp
go -C mosdns run ./cmd/dns-healthcheck --server 127.0.0.1:5353 --network tcp
```

Docker Compose 的常规入口为：

```bash
docker compose -f deploy/docker-compose.yml up
```

使用两个 CoreDNS mock upstream 的 Compose integration profile：

```bash
make configs
docker compose -f deploy/docker-compose.integration.yml --profile integration up --build
```

该 profile 仅发布 `5353/tcp` 与 `5353/udp`，不会占用生产 DNS 端口。镜像拉取若需代理，可设置 `ALL_PROXY=socks5://192.168.18.35:10808` 后执行命令。

Compose 只映射 `53/tcp`、`53/udp` 和 controller `8080/tcp`。运行前必须在 `deploy/secrets/mosdns_control_token` 写入随机共享 token；不得提交该运行时 secret。
