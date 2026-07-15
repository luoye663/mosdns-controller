# mosdns-manager

局域网 DNS 管理平台。`mosdns/` 是固定在官方 `v5.3.4` 的 GPL-3.0 数据面 fork；`controller/` 与 `web/` 是独立的控制面和管理员界面。

## 当前阶段

已完成 Phase 0 与 Phase 1：上游兼容性审计、可注册的 `dynamic_rule_engine` 和 `query_audit` skeleton、controller/WebUI 构建骨架、基础 CI 与 Compose 配置。规则编译、发布、认证、查询审计与正式 DNS 拓扑仍在后续阶段实现。

## 开发环境

- Go `1.26.5` 或兼容版本；上游 mosdns 的 `go.mod` 固定为 `1.24.9`，controller 使用 `1.25.0`。
- Node.js 22 与 npm。
- Docker Compose，用于后续容器验证。

```bash
npm --prefix web install
make test
make build
make race
```

WebUI 本地开发：

```bash
npm --prefix web run dev
```

`deploy/docker-compose.yml` 仅发布 DNS `53/udp`、`53/tcp` 和 controller 公共 `8080/tcp`。mosdns `9091` 与 controller ingest `8081` 只在 Compose 内部网络可达。

## 版本与许可证

- mosdns 基线：`v5.3.4`，commit `b7323188bab1ea742538aeccb31b692bc4967d1b`。
- 兼容性审计：[docs/mosdns-v5.3.4-compatibility.md](docs/mosdns-v5.3.4-compatibility.md)。
- GPL 边界：[docs/license-boundary.md](docs/license-boundary.md)。
- 不得提交 `deploy/secrets/` 中的真实 token、密码、私有 DoH URL 或 DNS 数据。
