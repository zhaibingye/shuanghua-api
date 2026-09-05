# Doubao Seedance 与 MediaKit

## 设计与启用

实现位于 `plugins/tasks/doubao/plugin.js`（工厂版本 **1.1.0**）。MediaKit 是现有
`doubao` 插件的可选流水线，不是另一个 Go TaskAdaptor，也不占用新的渠道类型。
这样普通 Seedance 与超分共用请求转换、模型映射、原生 API、OpenAI Video 和
Responses 展示，避免两个插件争抢同一模型或路由。

推荐在渠道管理中创建 **Task Plugin（59）** 渠道，选择 `doubao`：

- Base URL：`https://ark.cn-beijing.volces.com`，不要附加 `/api/v3`。
- 模型：选择插件支持的 Seedance 模型；自定义销售模型名可以通过渠道模型映射指向
  实际的 Seedance 模型或 Ark endpoint ID。
- 普通生成密钥：单个 Ark API Key，行为不启用超分。
- 超分密钥：`ARK_API_KEY|MEDIAKIT_API_KEY`，两边为不同服务的凭证。
- 也支持一行 JSON：

  ```json
  {"ark_api_key":"ARK_API_KEY","mediakit_api_key":"MEDIAKIT_API_KEY"}
  ```

现有 DoubaoVideo（54）与 VolcEngine（45）渠道仍由此插件处理视频任务。
**组合密钥只用于视频专用渠道**；不要在同时承载普通聊天请求的 VolcEngine 渠道上
使用组合密钥。批量/多密钥配置每行一个完整密钥对，JSON 必须保持为单行。

默认 MediaKit 地址为 `https://amk.cn-beijing.volces.com`。
同源代理可以在上述 JSON 中增加 `mediakit_base_url`，例如频道 Base URL 为
`https://proxy.example` 时设置 `https://proxy.example/mediakit`。
此地址必须是官方地址或渠道自身的 HTTP(S) origin，不接受客户端 metadata 覆盖。
其他服务主机需要管理员审查并上传修改过 `allowedHosts` 和凭证地址校验的插件版本；
不会为了跨域代理放宽宿主的凭证主机限制。

如果数据库中启用了旧版本的插件覆盖，先在任务插件页面检查当前生效版本；数据库覆盖
优先于工厂插件。不要覆盖同 key/version 的源码，使用版本激活/回滚流程。

## 请求与分辨率

入口沿用当前版本支持的：

- `POST /doubao/api/v3/contents/generations/tasks`：Ark 风格 JSON。
- `POST /v1/videos`：JSON 或 multipart 字段；上传文件不支持，图片/视频用 URL 引用。
- `POST /v1/responses`：支持 stream、sync、background 三种模式。
- 查询、鉴权及文件下载沿用宿主任务 API 与 artifact capability。

```json
{
  "model": "doubao-seedance-2-0-260128",
  "prompt": "A cinematic sunrise",
  "seconds": 5,
  "resolution": "720p",
  "generate_audio": false,
  "seed": 0
}
```

普通密钥：按请求分辨率生成，不调用 MediaKit。组合密钥：保留旧 fork 的分辨率策略：

| 请求 resolution | Ark 生成分辨率 | MediaKit 最终分辨率 |
| --- | --- | --- |
| `480p` | `480p` | `720p` |
| `720p`（默认） | `480p` | `1080p` |
| `1080p` | `720p` | `1080p` |

组合模式不支持 `4k`、draft 输出或 Ark `callback_url`。Ark 的完成通知不代表整个
超分任务已完成，因此组合任务须查询宿主状态。普通模式继续支持 Ark 的上述原生能力。

提交与计费使用同一个规范化请求：

- `seconds` / `duration` 是正整数，宿主上限 3600 秒，实际模型的更小上限仍由 Ark 校验。
- 默认明确发送 5 秒、720p（组合模式按上表转换）。
- `frames` 与 duration 互斥，范围 1–86400，估算时使用 `frames / 24`，不截掉小数秒。
- 当前插件不接受 `duration=-1`；请使用明确时长，避免绕过宿主正数用量校验。
- 顶层、metadata、multipart 都检查时长、帧数与数量上限，连被顶层覆盖的字段也检查。
- 不接受文本中的时长、帧率、分辨率等 `--duration` / `--rs` 参数，使用结构化字段，
  使 Ark 的实际请求与预扣依据一致。
- 参考图、参考视频、音频和 draft task 内容保留；draft 公共 ID 由宿主鉴权、锁定原渠道
  后转换为上游 ID。组合模式不允许生成仅供后续使用的 draft 输出。
- `seed: 0`、`generate_audio: false` 等显式零值不会丢失。

## 持久化流水线与失败恢复

使用 [v1 轮询状态契约](./v1.md#polling-contract)，每轮只返回一个 HTTP descriptor，
插件内没有网络 I/O：

1. 提交 Ark，保存普通的 Ark 上游任务 ID，以及私有 `state.phase = generation`。
2. 查询 Ark。成功后**不标记整个任务成功**，保存视频 URL、实际 token、实际输出时长，
   推进到 `enhancement_submit`。
3. 下一轮对 MediaKit `POST /api/v1/tools/enhance-video`，使用 `scene=aigc`、
   `tool_version=standard` 和目标分辨率。返回的 MediaKit ID 放入私有 state，
   推进到 `enhancement`。
4. 后续查询 `GET /api/v1/tasks/:id`，最终成功才发布增强视频并结算。

公共 task ID 和 Ark 上游 ID 始终不变。阶段、MediaKit ID 和用于重试的源视频地址
保存在宿主私有 PluginState，不编码到公共 ID、不输出到协议响应。凭证不放进插件 state。

MediaKit `client_token` 由 Ark ID 与目标分辨率确定，与密钥、进程和时间无关。
提交响应丢失、进程重启或持久化前崩溃后会用相同 token 重试；避免重复上游任务依赖
MediaKit 服务端遵守其 `client_token` 幂等语义。宿主终态 CAS 保证本地只结算/退款一次。

HTTP 401/403、429、5xx、网络异常、未知状态沿用宿主轮询失败计数；未知状态不会
伪装成 running。明确失败、成功却缺失视频 URL、宿主失败次数耗尽/超时最终走既有退款链。
原生状态投影也以宿主持久化状态为准，Ark 已生成但超分未完成时不会展示 succeeded。

## 统一表达式计费与前端展示

**没有插件内置价格表、额外隐藏收费或独立 Seedance 结算实现。**
在模型价格设置中选择任务用量表达式。现有可视化编辑器、价格页与日志读取插件的
`usageSchema` / `usageExamples`，无需另一套 Seedance 专属前端页面。

| 用量字段 | 含义 |
| --- | --- |
| `tokens` | Ark 计费 token，提交按尺寸/时长估算，最终按 Ark 实际值覆盖 |
| `resolution` | **Ark 源视频**分辨率，不能用最终超分分辨率替代 |
| `video_input` | `none` / `video`，用于配置参考视频的 token 价格档位 |
| `enhancement_seconds` | MediaKit 处理时长；普通生成为 0，组合任务最终按源视频实际时长 |
| `enhancement_resolution` | `none` / `720p` / `1080p`，用于超分价格档位 |

示例（**仅示范表达式，不代表厂商现价**：token 为 $10/百万，720p 超分 $0.005/秒，
1080p 超分 $0.01/秒）：

```text
u("enhancement_resolution") == "1080p" ? tier("1080p enhanced", u("tokens") * 10 / 1000000 + u("enhancement_seconds") * 0.01) : u("enhancement_resolution") == "720p" ? tier("720p enhanced", u("tokens") * 10 / 1000000 + u("enhancement_seconds") * 0.005) : tier("Ark", u("tokens") * 10 / 1000000)
```

请按实际模型、源分辨率、video_input 和最终分辨率设置真实价格。人民币报价先按站点
采用的汇率换算为 USD；宿主任务表达式结果就是 USD，不再整体除以一百万。

普通生成已有 token 表达式仍有效，新增字段不影响旧表达式。**仅启用组合密钥不会自动
把超分价格加入旧表达式或按次价格中**，上线前必须配置覆盖该服务成本的定价。推荐将
普通生成与超分作为不同的销售模型别名，以分别管理价格、分组与渠道。

宿主冻结提交时的表达式与用量，预扣、结算差额、失败全退、额度饱和审计均走原有链路。
参考视频暂按每个 15 秒保守预估输入 token；实际 Ark token 会覆盖估算。缺失或无效的
实际用量不替换已有估算；显式 0 token 是有效实际值。

## 旧 fork 迁移注意

- 不恢复旧独立价格表或数字渠道类型 62（当前上游已使用该编号），不静默改写数据库。
- 将旧 MediaKit 渠道手动配置为 Task Plugin（59）并绑定 `doubao`，搬迁密钥对和价格。
- 旧的 `dmk:v1:...` 复合 ID 不是新流水线格式；旧版在途任务应先在旧服务完成/对账，
  再迁移渠道。新实现不会猜测旧任务的阶段或自动重新提交收费任务。
- 新插件能继续查询没有 PluginState 的普通 Ark 在途任务；即使渠道后来配置了组合密钥，
  也不会给这些旧普通任务追加超分阶段。

验证覆盖插件入参、各请求格式、分辨率与计费一致性、真实 HTTP descriptor、SQLite
状态恢复、上游提交重试、最终结算/失败退款、终态 CAS、artifact 与原生状态投影。
这些是本地确定性测试，不替代持有真实凭证时的 Ark / MediaKit 联调。
