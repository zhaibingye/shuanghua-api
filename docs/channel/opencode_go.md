# OpenCode Go 兼容

OpenCode Go 要求请求携带稳定的每对话 `X-Opencode-Session`。仅修改 User-Agent 或给整个渠道设置一个固定 ID 都不能实现每对话会话识别。

## 配置

在 OpenAI（类型 1）或 Anthropic（类型 14）渠道的「高级设置 → 渠道额外设置」中开启「OpenCode Go 兼容」。旧渠道默认关闭。

API 配置位于渠道的 `settings` JSON 字符串（不是 `setting`）：

```json
{"opencode_go_compat":true}
```

不需要数据库迁移。其他渠道类型不能启用此选项。关闭开关不会删除现有的手动请求头覆盖配置。

该功能只为出站请求补充会话头，不修改请求体、鉴权、模型、渠道选择、渠道内密钥轮询或计费。它与「渠道亲和」及后台登录会话独立。

## 会话识别

优先级如下：

1. 已应用的出站请求头覆盖中的有效 `X-Opencode-Session`。
2. 客户端请求头：`X-Opencode-Session`、`X-Claude-Code-Session-Id`、`X-Pi-Session-Id`、`X-Session-Affinity`、Thread/Session/Conversation ID 请求头。Thread ID 比通用 Session ID 更具体时优先使用 Thread ID。
3. 请求体的明确会话字段：`session_id` / `sessionId` / `sessionID`、对应的 `metadata` 字段、`conversation` / `conversation.id`、`conversation_id`、`thread_id` 等。
4. Claude Code 的 `metadata.user_id`：解析其中 JSON 字符串的 `session_id`，或提取旧版 `_session_…` 后缀。不会发送其中的设备或账号标识。
5. 客户端 User-Agent 或 Originator 表明来自 Codex 时，使用其按对话设置的 `prompt_cache_key`。
6. Responses 请求的 `previous_response_id` 查询响应与会话的关联。
7. 无显式标识时，根据消息历史的最长公共前缀推断稳定 ID。

有效的显式 ID 原样使用（去掉首尾空格），须为不超过 256 字节的可打印 ASCII。空值、控制字符、非 ASCII 或超长值会被忽略并使用后续兜底，不会作为非法请求头发送。

不会使用一般的 `X-Request-ID`、`X-Client-Request-Id`、用户 ID、账号 ID 或后台登录会话推断对话。普通客户端的 `prompt_cache_key` 可能在不同对话间共享，也不会直接被当成会话 ID。

## 历史匹配

支持 Chat Completions、Anthropic Messages、Responses 完整历史，以及转换到这两种渠道的 Gemini 消息格式。识别基于转换和参数覆盖前的入站请求；流式与非流式行为一致。

- 正常追加消息时复用 ID。
- 历史中途发生分叉时，为新分支分配独立 ID；再次访问原分支仍使用原 ID。
- 有显式会话 ID 时，以客户端为准，不因内容变化替换该 ID。
- 仅有相同 system prompt 不足以合并对话。
- 指纹包含消息角色、正文、工具参数及调用关联、图片等结构化内容。不使用大内容的局部采样代替完整摘要。
- 缓存按 new-api 用户、API 令牌及渠道上游 Base URL 隔离；不包含模型名、渠道 ID 或上游密钥。同一上游切换模型、轮换密钥不改变识别范围。
- 同一次请求重试复用已经解析的 ID，不再次读取或移动共享请求体的游标。

## Redis 与资源限制

启用 Redis 时，前缀路径通过 Redis Lua 原子匹配和更新；前缀关联采用 1 小时滑动有效期，并通过 LRU 限制为最多 65,536 个前缀键。不同进程可以共享 Redis 中的路径，应用重启后仍可读取未过期的关联。

同时保留有界本地 LRU 镜像。未启用 Redis 时使用本地索引；Redis 出错时记录不含聊天内容或 ID 的警告，并降级使用本地索引，不因兼容功能的缓存故障拒绝正常转发。Redis 故障、关联过期或淘汰后，不保证原有分支 ID 仍可恢复。

缓存中仅保存摘要、会话 ID 和必要的关联信息，不长期保存聊天明文、API 密钥或用户标识明文。响应 ID 关联复用现有 Redis/内存混合缓存，采用 1 小时有效期，本地缓存有容量限制。

可选历史检查最多处理 8 MiB 请求体、1,024 条消息及每条消息 256 个内容块，结构化内容还有限制嵌套深度与节点数。超过限制不会改变原有请求体大小校验或拒绝转发，也不会截掉后半段历史来错误合并对话；无法识别时生成请求级 ID，并保证同一次请求的重试保持一致。

## Responses 增量对话

原生 Responses 响应的 ID 会在下发前绑定到本次实际使用的会话 ID；SSE 在 `response.created` 中暴露 ID 时即建立关联，不必等待整个流结束，也不缓冲流。

后续 `previous_response_id` 请求通过该关联继续使用相同会话 ID。未知或已过期的响应 ID 会按调用者、上游和该响应 ID 生成隔离的兜底 ID，不会仅凭相同的本轮输入将不同增量链合并。

这只是会话请求头兼容，不提供上游 Responses 状态存储，也不会使本来不支持 `previous_response_id` 的服务或协议转换支持增量对话。原有的渠道选择、故障切换和多密钥行为不变；若上游状态绑定特定账号，仍需自行配置合适的路由。

## 管理请求与局限

渠道测试、拉取上游模型列表及适用的余额查询同样补充会话头。没有用户对话的管理操作使用独立 ID，不加入用户聊天历史缓存；手动请求头覆盖仍优先。

无客户端标识时，历史匹配只能推断：两个内容完全相同的独立对话无法准确区分，删除或压缩旧历史也可能导致原会话无法匹配。需要跨压缩、跨不同上游地址或长时间稳定关联时，应让客户端明确发送每对话 ID。
