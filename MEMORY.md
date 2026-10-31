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
- 跨格式请求在缺少思考强度控制时，按最终目标补默认值：Chat、Responses、Claude 为 `xhigh`，Gemini 为 `high`；显式关闭和预算不被默认值覆盖。转换时保留原始档位，Gemini 将 `xhigh`/`max` 映射为 `high`，2.5 系列仍使用等价预算。`ConvertRequestVia` 用请求局部状态保留原始意图，避免中间格式降低档位或丢失精确预算；原生同格式请求仍走透传路径。验证入口为 `request_registry_test.go` 的 `TestConvertRequestReasoning*`。
- IR 内容块索引包含思维和正文；投影 Chat 流时，`tool_calls.index` 必须独立映射为每个 choice 内从零开始的连续索引。工具块开始事件的 `Event.ID` 是调用 ID，不能用作 Chat 响应 ID。回归入口为 `TestChatToolStreamIndexesAndResponseIDAfterThinking`，并已用 OpenAI Python SDK 2.9.0 重放实际转换输出。
- 历史思维摘要不能直接充当目标协议的原生回放状态。Claude 请求只回传带签名或非空 redacted 数据的思维块；Responses 请求保留原始 reasoning item（含 ID 和加密状态），不为其他协议的摘要伪造 reasoning item。无法回传的摘要在请求转换时省略，并返回 `historical_thinking_omitted` 诊断；响应侧展示仍保留。回归入口为 `TestConvertRequestOmitsUnreplayableHistoricalThinking` 和 Responses 请求 roundtrip 测试。
- Responses 的结束状态在 IR 解析层统一映射，流式与非流式复用 `ir.FinishFromResponses`。内容过滤不能当作长度截断，Gemini 的正常工具调用应以 `STOP` 结束，`MALFORMED_FUNCTION_CALL` 应作为上游错误。
- Responses 的 item 完成事件和 response 完成事件会重复携带工具快照；转换为增量协议时，函数名及调用 ID 只能发送一次。无效 `input` 结构应在解析边界报错，不应静默变成空对话。验证入口为 `relaykit/relayconvert/{request,response}_registry_test.go`。
- Chat/Claude/Responses→Gemini 按项目兼容策略，仅在会保留普通函数或可转换的自定义工具时过滤内置搜索；仅搜索时保留，Gemini 原生请求不变。`attachGeminiRequest` 预先检查全部工具，避免声明顺序影响结果，也不将已丢弃的工具当作过滤条件。过滤返回 `hosted_web_search_omitted` 语义损失诊断，默认允许，`safe`/`strict` 仍拒绝。回归入口为 `TestConvertRequestToGeminiToolCombination` 和 `TestResponsesToGeminiOmitsBuiltInWebSearch`。

## Windows 本机竞态验证

- 已通过 WinGet 安装 `BrechtSanders.WinLibs.POSIX.UCRT`（GCC 16.2.0），用户 PATH 已包含工具链目录，新终端生效。Go 当前默认关闭 CGO；运行竞态检测时在进程环境设置 `CGO_ENABLED=1`，并用 `GOMAXPROCS=2`、`go test -race -p 1` 控制编译并行度。`common`、`relay/helper`、`service`、`relay/channel`、`relay/common` 整包竞态测试已通过。
