# GPL 许可边界

`mosdns/` 是官方 mosdns `v5.3.4` 的 fork，继续受 GPL-3.0 约束。修改后的 mosdns 二进制分发时，必须同时满足 GPL-3.0 对相应源码提供和修改说明的要求。

`controller/` 和 `web/` 通过 HTTP 在独立进程中与 mosdns 协作；它们不导入、链接或复制 mosdns 的 Go 源码。两者可采用独立许可证，但不得把 `mosdns/plugin/executable/` 的实现复制到控制面代码。

版本、构建参数和 API 契约可在组件间共享。规则快照和查询事件通过运行时协议传递，不改变上述进程与许可证边界。
