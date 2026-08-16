# 故障排查

| 症状 | 检查与处理 |
|---|---|
| DNS 无响应 | `docker compose ps`、mosdns 日志和 UDP/TCP healthcheck。确认容器仅有 `NET_BIND_SERVICE`，没有其它 capability。 |
| WebUI 显示 mosdns 不可用 | 检查内部网络、共享 token 文件权限和 controller 日志；不要把 `9091` 映射到宿主机。 |
| 规则发布 UNKNOWN | 使用“运行时对账”，不要直接重新发布。超时可能意味着 mosdns 已成功应用快照。 |
| 新 block 未生效 | 确认规则为 enabled，且动态引擎位于所有 cache 前；查看当前 snapshot version。 |
| 上游组切换后仍有旧答案 | 检查目标上游组的 cache flush 日志和 shared bearer token。 |
| 查询日志停止 | 检查 controller `8081` 内部连通性、ingest queue 和 SQLite 容量。日志可丢弃，但 DNS 不应因此失败。 |
| 数据库接近上限 | 查看系统状态，确认 retention 任务；先清理 raw queries，绝不删除 active rule 或 admin 数据。 |
| 恢复后版本不一致 | 按 recovery 文档运行 reconcile；未知更高 mosdns 版本必须人工处理。 |
