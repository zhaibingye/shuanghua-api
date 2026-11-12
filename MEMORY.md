# new-api 工程记忆

## 转发运行时

- 流式转发由 `relay/helper/stream_scanner.go` 的请求处理协程统一调用数据回调、发送心跳和记录终态；上游读取协程只传递原始行与读取结果。接管流之前须等待请求准备阶段的 `streamKeepAliveWriter` 心跳退出，再修改响应头。必须先处理已读取的数据，再记录 EOF，避免正常结束掩盖回调错误。取消请求要立即关闭上游，即使下游回调尚未返回；返回前须等待关闭与读取完成。
- SSE 写入由 `SetEventStreamHeaders` 安装的 writer 按累计写入字节数延长截止时间，避免大图事件被固定超时截断。流扫描器只把完整的裸 `[DONE]` 或 `data: [DONE]` 当作结束。
- HTTP 代理配置只在构造连接池前校验一次，再通过 `http.Transport.Clone` 创建独立分片。保留各分片的代理、TLS 与协议策略；不要使用吞掉代理构造错误的工厂或不会发生的直连降级分支。验证入口为 `service/http_client_transport_test.go`。
- 请求体存储的游标、关闭状态与释放计数由同一把互斥锁管理。磁盘构造失败须清理临时文件，`Bytes()` 不得改变主读取游标，独立回放 reader 不得接管存储生命周期。验证入口为 `common/body_storage_test.go`。
- 日志轮转计数由 `atomic.Uint64` 管理，文件切换由 `setupLogLock` 串行化；计数允许近似并不意味着可以无锁读写。`relay/helper` 与 `service` 的并发日志路径会在竞态检测中覆盖该约束。

## 内容审核

- 严重级别分数阈值以单个 `ContentModerationScoreThresholds` JSON 选项保存，要求 `0 <= medium < high < critical <= 1`；缺省仍为 0.50/0.75/0.90，比较包含边界。前置与后置审核复用同一判定函数，有命中分类时只取这些分类的分数，同时兼容只返回整体违规标记的服务。
- 自动审核缓存绑定内容、上游地址、模型及阈值策略，缓存命中仍须记录当前请求的违规事件。人工复核使用独立的 `resolved:` 内容指纹缓存，保留原有 24 小时放行优先级。图片审核按实际输入发送，不依据模型名称猜测是否应丢弃图片。

## API 协议转换

### 转换入口与缓存

- 内置文本转换器的实际入口在 `relaykit/relayconvert/text_converter_registry.go`，请求与响应统一走 `ir/project`。排查旧 `internal` 转换器时先确认调用链。
- Chat、Responses 与 Claude 转换用原始 JSON 传递结构化输出和工具 schema。转成 `map[string]any` 再编码会排序，影响上游按属性顺序生成结果。回归入口为 `TestConvertRequestPreservesSchemaPropertyOrder`。
- 缓存计数必须同时保留公共 `PromptTokensDetails` 与 Responses 的 `InputTokensDetails`：`UsageFromChatUsage` 也用于其他目标协议和流式账务，不能只填 Responses 别名。Gemini 投影须发送 `cachedContentTokenCount`，并把已包含在 completion 总数里的 reasoning 拆为 `thoughtsTokenCount`。原始供应商账务仍以 `BillingUsage` 快照为准。回归入口为 `relaykit/relayconvert/cache_conversion_test.go`。
- Chat/Responses 共有的 `prompt_cache_key`、`prompt_cache_retention`、`cache_salt` 经 IR 保留。目标无法表示的缓存字段和 Responses 非字符串缓存 key 静默丢弃。原生请求体透传保留原字段。该策略仅用于缓存字段。
- Claude 工具调用、工具定义及工具结果中的缓存断点均需保留。单文本工具结果只有在不含 `cache_control` 或 citations 时才能折叠成字符串。`ConvertRequestVia` 抽取并最终恢复的工具定义也必须保留缓存配置。

### 思考参数与流式响应

- 跨格式转换缺少思考强度控制时，Chat、Responses、Claude 默认 `xhigh`，Gemini 默认 `high`。回归入口为 `TestConvertRequestReasoningDefaultsAndEquivalentLevels`。
- 按目标模型支持的形式发送思考参数。档位模型只收到正数预算时，Gemini 优先用 `high`（Gemini 4 输出 `HIGH`），OpenAI Chat、Responses 和自适应 Claude 优先用 `xhigh`，并按模型支持范围回退。档位与预算同传时优先使用档位。Gemini 2.5 将档位投影为预算，旧 Claude 手动模式和原生 Qwen 支持预算。`ConvertRequestVia` 保留原始意图和精确预算，请求体透传绕过改写。回归入口为 `TestReasoningBudgetsInNativeUpstreamRequests` 和 `TestConvertRequestReasoningExplicitDisableAndBudget`。
- Gemini 4 Argon 的原生 `thinkingBudget` 会触发 400，`thinkingLevel` 接受 `LOW`、`MEDIUM`、`HIGH`，服务端缺省为 `MEDIUM`。渠道 429 需按实际转发体单独排查。
- IR 内容块索引包含思维和正文。Chat 流的 `tool_calls.index` 要为每个 choice 从零连续编号，工具块开始事件的 `Event.ID` 是调用 ID。回归入口为 `TestChatToolStreamIndexesAndResponseIDAfterThinking`。
- 历史思维摘要不能直接充当目标协议的原生回放状态。Claude 请求只回传带签名或非空 redacted 数据的思维块；Responses 请求保留原始 reasoning item（含 ID 和加密状态），不为其他协议的摘要伪造 reasoning item。无法回传的摘要在请求转换时省略，并返回 `historical_thinking_omitted` 诊断；响应侧展示仍保留。回归入口为 `TestConvertRequestOmitsUnreplayableHistoricalThinking` 和 Responses 请求 roundtrip 测试。
- Responses 的结束状态在 IR 解析层统一映射，流式与非流式复用 `ir.FinishFromResponses`。内容过滤不能当作长度截断，Gemini 的正常工具调用应以 `STOP` 结束，`MALFORMED_FUNCTION_CALL` 应作为上游错误。
- Responses 的 item 完成事件和 response 完成事件会重复携带工具快照；转换为增量协议时，函数名及调用 ID 只能发送一次。无效 `input` 结构应在解析边界报错，不应静默变成空对话。验证入口为 `relaykit/relayconvert/{request,response}_registry_test.go`。

### Gemini 原生请求与工具

- 关闭请求体透传时，Gemini 原生请求会经 DTO 解码和适配器重新编码。`tools` 保留原 JSON 键名，`toolConfig` 把 `function_calling_config` 和已知子字段别名转成 REST 字段并保留显式 `false`。排查时应检查实际出站 JSON。
- Gemini 类型的原生生成路由在关闭透传时移除客户端系统提示里的 `<available_skills>` 标签，保留内部文本和函数声明。同一 Argon 渠道重放时，技能系统提示段触发了 429，单独加入 `read_file` 声明为 200。移除标签后，原失败请求、工具调用和带思考签名的结果回传均通过。上游判定原因尚未确认。回归入口为 `TestNativeGeminiSearchAndFunctionTools`。
- 保留普通函数或可转换的自定义工具时过滤内置网页搜索。Chat/Claude/Responses→Gemini 走 `attachGeminiRequest`，Gemini 类型的原生路由在关闭透传时直接过滤工具 JSON 中的搜索键。两条路径均记录 `hosted_web_search_omitted`，默认允许，`safe`/`strict` 拒绝。回归入口为 `TestConvertRequestToGeminiToolCombination` 和 `TestNativeGeminiSearchAndFunctionTools`。

## 余额查询接口

- `/v1/credits` 返回账户余额，采用站点显示单位。`/api/usage/token/` 返回 API Key 的原始额度，启用 `allow_user_quota_query` 时附带 `data.user_quota`。OpenMinis `1a71df0` 的 New API 解析器只读取 Key 额度字段。
- OpenMinis 优先探测 `GET /v1/usage`，按 Sub2API 钱包格式读取顶层 `isValid` 和 `balance`，固定以 USD 展示。已识别为 New API 的连接需重新探测。网关要求 `allow_user_quota_query`，按通过鉴权的 Key 确定账户，用原始剩余额度除以 `QuotaPerUnit`。回归入口为 `router/relay_router_test.go` 的 `TestSub2APIWalletBalanceReturnsAccountQuotaInUSD` 和 `TestBalanceEndpointsEnforceTokenRestrictions`。

## 视频插件兼容路由

- `SetVideoRouter` 的 Ark 和 xAI 提交别名沿用共享视频协议选渠道，Ark 路径可选中已绑定的 MediaKit 渠道。插件原生协议仍可通过 `/doubao`、`/doubao-mediakit` 和 `/xai` 前缀访问。`GET /v1/videos/:task_id` 依据已保存的提交路径转交原生查询；缺少执行快照或使用 xAI 插件 1.0/1.1 的历史任务走旧渲染。回归入口为 `TestBuiltInPluginRoutesAreServedBesideHostRoutes` 和 `TestGetOpenAIVideoRouteHandsNativeXAITaskToPluginQuery`。

## 测试与竞态验证

- `relay/channel` 的 Gin 模式在 `TestMain` 中初始化，避免并行测试重复写全局模式。后台任务轮询会通过 ORM 更新传入的任务对象，测试应在启动轮询前保存用于断言的任务 ID。
- `controller/relay_stream_test.go` 的保活测试关闭性能指标并在清理时恢复，避免异步 Redis 上报与后续测试替换 Redis 配置发生竞态。
- Windows 竞态测试使用 WinLibs GCC（`BrechtSanders.WinLibs.POSIX.UCRT`），在当前进程启用 `CGO_ENABLED=1`。本机用 `GOMAXPROCS=2` 和 `go test -race -p 1` 限制并行编译。
