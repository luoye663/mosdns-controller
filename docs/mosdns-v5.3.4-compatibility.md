# mosdns v5.3.4 兼容性审计报告

## 范围与来源

本报告以本地检出的官方 mosdns 源码为依据，不依赖 Wiki。

| 项目 | 值 |
|---|---|
| 上游仓库 | `https://github.com/IrineSistiana/mosdns.git` |
| 固定标签 | `v5.3.4` |
| 实际 commit | `b7323188bab1ea742538aeccb31b692bc4967d1b` |
| Commit 时间 | `2026-01-11T19:09:51+08:00` |
| Go module | `github.com/IrineSistiana/mosdns/v5` |
| 上游 Go directive | `go 1.24.9` |
| 审计命令 | `go test ./...` |
| 审计结果 | 在 Go `1.26.5 linux/amd64` 上通过 |

本地 fork 位于 `mosdns/`，并固定在上述 commit。除非先完成显式升级审计，后续实现不得改变该基线。

## 插件注册与初始化

实际注册文件是 [`mosdns/plugin/enabled_plugins.go`](../mosdns/plugin/enabled_plugins.go)，文件名为复数，而不是规格中所写的 `plugin/enabled_plugin.go`。该文件通过匿名导入加载内置插件；项目的两个插件应添加如下导入：

```go
_ "github.com/IrineSistiana/mosdns/v5/plugin/executable/dynamic_rule_engine"
_ "github.com/IrineSistiana/mosdns/v5/plugin/executable/query_audit"
```

每个可配置插件都在 `init` 中注册，最小模式参见 [`plugin/executable/sleep/sleep.go`](../mosdns/plugin/executable/sleep/sleep.go)：

```go
coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
```

准确的初始化函数类型定义于 [`coremain/plugin.go`](../mosdns/coremain/plugin.go)：

```go
type NewPluginFunc func(bp *BP, args any) (p any, err error)
```

`coremain.BP` 提供 `L() *zap.Logger`、`M() *Mosdns`、`Tag() string` 和 `RegAPI(mux *chi.Mux)`。调用 `Init` 前，配置参数会由 `utils.WeakDecode` 解码。插件必须在 `Init` 内完成配置校验；初始化失败时必须清理已创建的部分资源。

## Sequence 执行契约

[`plugin/executable/sequence/iface.go`](../mosdns/plugin/executable/sequence/iface.go) 定义了两种可执行插件接口：

```go
type Executable interface {
    Exec(ctx context.Context, qCtx *query_context.Context) error
}

type RecursiveExecutable interface {
    Exec(ctx context.Context, qCtx *query_context.Context, next ChainWalker) error
}
```

`dynamic_rule_engine` 必须实现 `sequence.Executable`：它只分类请求、写入不可变决策元数据和 marks，然后返回，不生成 DNS 响应。`query_audit` 必须实现 `sequence.RecursiveExecutable`，从而可以先执行其余 sequence，再读取最终结果。

[`plugin/executable/sequence/chain.go`](../mosdns/plugin/executable/sequence/chain.go) 表明 `ChainWalker.ExecNext` 对 `Executable` 执行后会继续后续节点，而对 `RecursiveExecutable` 会直接返回。因此后置观察插件必须自行调用 `next.ExecNext`。

[`plugin/executable/sequence/built_in.go`](../mosdns/plugin/executable/sequence/built_in.go) 确认目标配置使用的控制流语义：

| 操作 | 实际行为 |
|---|---|
| `accept` | 立即返回 `nil`，当前 chain 的后续节点不再执行。 |
| `reject <rcode>` | 在 `qCtx` 写入响应后返回 `nil`。 |
| `return` | 恢复到 `jump` 的调用方；根 chain 中则结束。 |
| `goto <sequence>` | 创建没有返回目标的新 walker；调用方的后续节点被跳过。 |
| `jump <sequence>` | 执行目标 sequence；目标结束后回到调用方。 |

规格要求的 `route_local` 和 `route_remote` 必须定义为独立带 tag 的 `sequence` 插件，随后可作为有效的 `goto` 目标。

## query_audit 后置观察

[`plugin/executable/query_summary/query_summary.go`](../mosdns/plugin/executable/query_summary/query_summary.go) 是必须复用的执行模型：

```go
func (p *Plugin) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
    started := time.Now()
    err := next.ExecNext(ctx, qCtx)
    // qCtx 此时包含最终响应、marks 与已保存的 metadata。
    p.enqueueNonBlocking(qCtx, started, err)
    return err
}
```

实际实现中，该函数不得调用 controller、执行阻塞 I/O，或等待已满队列。它必须构造有大小上限的事件，并用带 `default` 的 `select` 非阻塞入队。由于外层递归可执行插件仍在调用栈上，该模型能观察到 `goto`、`jump`、`accept` 和 `reject` 后的结果。

顶层服务行为在 [`pkg/server_handler/entry_handler.go`](../mosdns/pkg/server_handler/entry_handler.go)：它创建一个 `query_context.Context`，设置 `ServerMeta`，调用入口 sequence；执行错误转换为 `SERVFAIL`，没有响应则转换为 `REFUSED`。因此 `query_audit` 推导最终事件时必须同时使用返回的 `err` 和 `qCtx.R()`。

## 请求上下文与 Metadata

[`pkg/query_context/context.go`](../mosdns/pkg/query_context/context.go) 已提供请求生命周期内的存储接口：

```go
func RegKey() uint32
func (ctx *Context) StoreValue(k uint32, v any)
func (ctx *Context) GetValue(k uint32) (any, bool)
func (ctx *Context) SetMark(m uint32)
func (ctx *Context) HasMark(m uint32) bool
func (ctx *Context) DeleteMark(m uint32)
```

在包级别注册一个 metadata key：

```go
var runtimeDecisionKey = query_context.RegKey()
```

`dynamic_rule_engine` 应保存不可变值，推荐使用值类型 struct，或保存后绝不修改的指针：

```go
type RuntimeDecision struct {
    SnapshotVersion uint64
    AccessRuleID    int64
    RouteRuleID     int64
    LoggingRuleID   int64
    AccessAction    string
    RouteAction     string
    RouteSource     string
}
```

`query_audit` 在 `next.ExecNext` 返回后读取该 key。该方案对单次请求生命周期可靠，且不需要全局 request-ID map。`Context` 的方法明确不支持并发调用；`Context.Copy` 会复制 key map 但不会深拷贝 value，因此 metadata 必须保持不可变。cache 的 lazy refresh 可能把 Context 副本交给后台 goroutine。

同一文件还提供 `Q()`、`QQuestion()`、`R()`、`StartTime()`、`ServerMeta`、`ClientOpt()`、`RespOpt()` 与 `UpstreamOpt()`。`ServerMeta` 是 [`pkg/server/iface.go`](../mosdns/pkg/server/iface.go) 中 `server.QueryMeta` 的别名，包含 `ClientAddr netip.Addr` 与 `FromUDP bool`。标准 UDP 与 TCP server 会填写这些元数据。

## 域名匹配与规范化

[`plugin/data_provider/domain_set/domain_set.go`](../mosdns/plugin/data_provider/domain_set/domain_set.go) 使用 `domain.NewDomainMixMatcher()`，并暴露 `DomainMatcherProvider`。匹配器接口在 [`pkg/matcher/domain/interface.go`](../mosdns/pkg/matcher/domain/interface.go)。

上游 matcher 可用于编译后的不可变快照：

```go
m := domain.NewMixMatcher[CompiledDecision]()
m.GetSubMatcher(domain.MatcherDomain).Add(pattern, decision)
```

上游 `MixMatcher.Match` 的匹配类型顺序是 full、domain、regexp、keyword。其 domain matcher 采用反向标签树，并返回最深的已存储 domain 匹配。这与项目规则排名中的匹配类型和 domain 深度部分一致。

但上游 matcher 不能独立构成完整的项目规则引擎：

- [`pkg/matcher/domain/utils.go`](../mosdns/pkg/matcher/domain/utils.go) 的 `NormalizeDomain` 只会转小写并去除末尾点；不会 trim 空白、使用 `idna.ToASCII` 转换 IDN，也不校验 DNS/标签长度。
- `RegexMatcher` 由 Go map 支撑，返回第一个迭代命中的规则，无法保证 priority 与项目并列规则语义的确定性。
- matcher value 必须能表示同一个匹配节点上的各类别最终决策。编译器必须在发布不可变快照之前解决排序和 local/remote 冲突。
- 项目仅允许 `full`、`domain` 与 `regexp`；不得调用或暴露 `MatcherKeyword`。

编译器应先执行项目自身的规范化和校验，再把 full/domain 决策编译到不可变 map 或上游 matcher。regexp 应使用按确定性规则排序的不可变 slice。这不要求实现自定义 Trie。

[`plugin/matcher/qname/qname.go`](../mosdns/plugin/matcher/qname/qname.go) 确认 QNAME 匹配读取 `qCtx.Q().Question` 中的 `question.Name`；`EntryHandler` 已保证正常 server 请求恰好包含一个 question。

## 插件 HTTP API 与 Cache Flush

[`coremain/mosdns.go`](../mosdns/coremain/mosdns.go) 通过下列方式挂载插件 router：

```go
m.httpMux.Mount("/plugins/"+tag, mux)
```

因此 `bp.RegAPI(router)` 是动态规则 API 的正式扩展方式。tag 为 `dynamic_rules` 的插件应创建一个 `chi.Router`，注册 `GET /status`、`POST /validate`、`PUT /snapshot` 和 `POST /match`；最终路径为 `/plugins/dynamic_rules/...`。

[`plugin/executable/reverse_lookup/reverse_lookup.go`](../mosdns/plugin/executable/reverse_lookup/reverse_lookup.go) 展示了相同的 API 注册模式。core 没有全局插件 API 认证 middleware。`dynamic_rule_engine` 必须在调用 `bp.RegAPI` 前给其所有 handler 或 router 套上自己的 Bearer Token middleware。这是实现 token 文件读取、`Authorization` 解析、`subtle.ConstantTimeCompare`、请求体限制和统一 `401` 响应的位置；日志中不得输出凭证。

[`plugin/executable/cache/cache.go`](../mosdns/plugin/executable/cache/cache.go) 通过 `bp.RegAPI(c.Api())` 注册 API。实际 flush 端点是 **`GET /plugins/<cache-tag>/flush`**，它调用 `c.backend.Flush()`，且没有内建认证和响应体。

该上游行为与项目“全部 mosdns/controller 内部 API 使用共享 Bearer Token”的安全要求冲突。仅不将 `9091` 映射到宿主机是必要条件，但并不充分。Phase 5 必须保留 `GET` 方法，并在 fork 的 cache API 中加入 constant-time Bearer 验证，或提供等效且范围严格受限的 mosdns API guard，再让 controller 调用两个 cache flush 端点。不得将上游未认证端点视为最终设计。

## 生命周期与指标

[`coremain/mosdns.go`](../mosdns/coremain/mosdns.go) 会在进程关闭信号后关闭所有实现 `io.Closer` 的已配置插件；它不提供插件专属 context。拥有 goroutine 的插件必须实现：

```go
func (p *Plugin) Close() error
```

`Close` 必须取消其持有的工作、恰好一次关闭内部通知 channel，并在配置的有限关闭时间内等待 worker。不能在 DNS 请求 goroutine 仍可能发送时关闭 producer queue；应通过取消信号与 `WaitGroup` 明确 producer/worker 的关闭顺序。

[`plugin/executable/cache/cache.go`](../mosdns/plugin/executable/cache/cache.go) 展示了指标注册约定：

```go
registerer := prometheus.WrapRegistererWithPrefix(
    PluginType+"_", bp.M().GetMetricsReg(),
)
```

`Mosdns.GetMetricsReg` 已添加 `mosdns_` 前缀。应按需使用 `prometheus.NewCounter`、`NewGauge`、`NewGaugeFunc` 与 histogram；如果指标可能来自同类插件的多个实例，则添加 `tag: bp.Tag()` 常量标签。必须在 `Init` 中注册 collector，并返回注册错误，不能忽略重复注册失败。

## 最小可编译骨架

下列接口是 Phase 1 的最小插件骨架；它们有意不包含属于后续阶段的业务功能。

```go
// plugin/executable/dynamic_rule_engine/plugin.go
package dynamic_rule_engine

import (
    "context"

    "github.com/IrineSistiana/mosdns/v5/coremain"
    "github.com/IrineSistiana/mosdns/v5/pkg/query_context"
    "github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

const PluginType = "dynamic_rule_engine"

type Args struct{}

type Plugin struct{}

var _ sequence.Executable = (*Plugin)(nil)

func init() {
    coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

func Init(_ *coremain.BP, _ any) (any, error) { return &Plugin{}, nil }

func (p *Plugin) Exec(_ context.Context, _ *query_context.Context) error { return nil }
```

```go
// plugin/executable/query_audit/plugin.go
package query_audit

import (
    "context"

    "github.com/IrineSistiana/mosdns/v5/coremain"
    "github.com/IrineSistiana/mosdns/v5/pkg/query_context"
    "github.com/IrineSistiana/mosdns/v5/plugin/executable/sequence"
)

const PluginType = "query_audit"

type Args struct{}

type Plugin struct{}

var _ sequence.RecursiveExecutable = (*Plugin)(nil)

func init() {
    coremain.RegNewPluginFunc(PluginType, Init, func() any { return new(Args) })
}

func Init(_ *coremain.BP, _ any) (any, error) { return &Plugin{}, nil }

func (p *Plugin) Exec(ctx context.Context, qCtx *query_context.Context, next sequence.ChainWalker) error {
    return next.ExecNext(ctx, qCtx)
}
```

当骨架开始创建 worker 时，必须增加 `var _ io.Closer = (*Plugin)(nil)` 并实现有界清理。动态 API 端点加入时，`Init` 必须创建 `chi.Router`、注册带认证的 handler，并且只调用一次 `bp.RegAPI(router)`。

## 规格修正项

1. 将 Phase 1 中每处 `mosdns/plugin/enabled_plugin.go` 改为 `mosdns/plugin/enabled_plugins.go`。
2. cache flush 的现有端点是 `GET /plugins/cache_local/flush` 和 `GET /plugins/cache_remote/flush`，不是 `POST`。上游端点未认证，不符合项目安全要求；Phase 5 必须在保留方法的同时增加共享 token 校验。`9091` 必须仅限 Docker 内部网络。
3. 不得依赖上游 `NormalizeDomain` 完成项目规则校验。编译器必须在构建不可变快照前自行实现 trim、IDNA ASCII 转换、DNS 标签校验与 regexp 例外处理。
4. 使用 `query_context.RegKey` 和不可变 `StoreValue` 数据传递 rule ID 与 snapshot version。不需要，也不得使用 core metadata 扩展或全局 `sync.Map`。
5. 实测上游 `go.mod` directive 为 Go `1.24.9`，而项目 controller 规格要求 Go `1.25`。Phase 1 的构建镜像必须同时满足两者，并记录实际工具链。
6. 后续阶段必须添加 API 挂载、cache flush 方法、以及 `goto`、`accept`、`reject` 后 query audit 观察结果的契约测试。插件 API 是实验性接口，本报告仅建立固定标签基线。

## 已知限制

本阶段仅验证上游源码结构及其现有测试套件。尚未验证自定义插件行为、认证、真实 cache 拓扑中的 metadata 传递或 Docker 部署；这些内容属于 Phase 1 至 Phase 5 的验收范围。
