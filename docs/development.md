# 开发说明

## 固定基线

`mosdns/` 必须停留在官方 `v5.3.4` commit `b7323188bab1ea742538aeccb31b692bc4967d1b`。升级前必须更新兼容性报告并运行对应契约测试。

## 构建与测试

先安装 WebUI 依赖，然后运行：

```bash
npm --prefix web install
make test
make build
make race
```

Phase 1 的 race 命令已覆盖两个插件 skeleton 和 controller。后续行为变更必须先补充最窄范围测试，再执行受影响的更广测试。

## 密钥与端口

真实 secret 放在 `deploy/secrets/`，该目录只保留 `.gitkeep`。Compose 不得发布 mosdns `9091` 或 controller `8081`；仅 `53/udp`、`53/tcp`、`8080/tcp` 可发布到宿主机。
