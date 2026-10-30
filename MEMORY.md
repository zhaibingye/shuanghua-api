# new-api 工程记忆

## 当前仓库的已验证约束

- 流式转发由 `relay/helper/stream_scanner.go` 的请求处理协程统一调用数据回调、发送心跳和记录终态；上游读取协程只传递原始行与读取结果。接管流之前须等待请求准备阶段的 `streamKeepAliveWriter` 心跳退出，再修改响应头。必须先处理已读取的数据，再记录 EOF，避免正常结束掩盖回调错误。取消请求要立即关闭上游，即使下游回调尚未返回；返回前须等待关闭与读取完成。
- HTTP 代理配置只在构造连接池前校验一次，再通过 `http.Transport.Clone` 创建独立分片。保留各分片的代理、TLS 与协议策略；不要使用吞掉代理构造错误的工厂或不会发生的直连降级分支。验证入口为 `service/http_client_transport_test.go`。
- 请求体存储的游标、关闭状态与释放计数由同一把互斥锁管理。磁盘构造失败须清理临时文件，`Bytes()` 不得改变主读取游标，独立回放 reader 不得接管存储生命周期。验证入口为 `common/body_storage_test.go`。
- 日志轮转计数由 `atomic.Uint64` 管理，文件切换由 `setupLogLock` 串行化；计数允许近似并不意味着可以无锁读写。`relay/helper` 与 `service` 的并发日志路径会在竞态检测中覆盖该约束。
- `relay/channel` 的 Gin 模式在 `TestMain` 中统一初始化，禁止并行测试重复修改全局模式。任务轮询会通过 ORM 更新传入的任务对象，测试需在启动后台轮询前保存用于断言的任务 ID，避免并发读取正在更新的对象。
- `controller/relay_stream_test.go` 的保活测试显式关闭无关的性能指标并恢复配置，避免异步 Redis 上报跨越测试边界，与后续 fixture 替换 Redis 配置发生竞态。

## 内容审核

- 严重级别分数阈值以单个 `ContentModerationScoreThresholds` JSON 选项保存，要求 `0 <= medium < high < critical <= 1`；缺省仍为 0.50/0.75/0.90，比较包含边界。前置与后置审核复用同一判定函数，有命中分类时只取这些分类的分数，同时兼容只返回整体违规标记的服务。
- 自动审核缓存绑定内容、上游地址、模型及阈值策略，缓存命中仍须记录当前请求的违规事件。人工复核使用独立的 `resolved:` 内容指纹缓存，保留原有 24 小时放行优先级。图片审核按实际输入发送，不依据模型名称猜测是否应丢弃图片。

## API 协议转换

- 内置文本转换器的实际入口在 `relaykit/relayconvert/text_converter_registry.go`，请求与响应统一走 `ir/project`；排查旧 `internal` 转换器时须先确认调用链，不能仅凭旧实现判断当前行为。
- Responses 的结束状态在 IR 解析层统一映射，流式与非流式复用 `ir.FinishFromResponses`。内容过滤不能当作长度截断，Gemini 的正常工具调用应以 `STOP` 结束，`MALFORMED_FUNCTION_CALL` 应作为上游错误。
- Responses 的 item 完成事件和 response 完成事件会重复携带工具快照；转换为增量协议时，函数名及调用 ID 只能发送一次。无效 `input` 结构应在解析边界报错，不应静默变成空对话。验证入口为 `relaykit/relayconvert/{request,response}_registry_test.go`。

## Windows 本机竞态验证

- 已通过 WinGet 安装 `BrechtSanders.WinLibs.POSIX.UCRT`（GCC 16.2.0），用户 PATH 已包含工具链目录，新终端生效。Go 当前默认关闭 CGO；运行竞态检测时在进程环境设置 `CGO_ENABLED=1`，并用 `GOMAXPROCS=2`、`go test -race -p 1` 控制编译并行度。`common`、`relay/helper`、`service`、`relay/channel`、`relay/common` 整包竞态测试已通过。
