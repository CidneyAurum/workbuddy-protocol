# WorkBuddy 网关协议说明

本文描述 2026-10-28 本次导出源码快照的实际行为，供阅读、兼容性审查和离线测试使用。它不是 WorkBuddy / CodeBuddy 官方 API 规范，也不保证这些未公开接口在其他账号、站点或版本中可用。源码注释中关于上游限制、性能和费用的历史结论，未经本次联网实测；与函数实现不一致时，以下说明以实现为准。

文中的路径均相对仓库根目录。示例不含真实账号、令牌、设备数据或对话。

本地源码包含后续 Messages 兼容模块，并非某个上游 commit 的原封副本。为让独立仓库能构建，导出时将 `go.mod` 中 `localapi/anthropiccompat` 的仓库外相对路径改为 `./lib/anthropiccompat`，同时收录该模块的 4 个文件。这是源码整理的构建路径调整；没有因此修改生产服务、账号状态或协议处理行为。

## 1. 两层鉴权与请求流程

```text
调用方 → 本地 OpenAI 兼容接口 → 本地 API key 校验
       → 模型 realm 解析 → 会话与账号池选取 → 必要时刷新账号 token
       → 请求体适配、上游请求头 → 上游 SSE
       → SSE 规范化，或聚合为非流式 JSON
```

本地 `api_key` 只用于客户端访问网关；账号 `accessToken` / `refreshToken` 用于网关访问上游，二者互不替代。`internal/server/handler.go` 的 `withAuth` 校验精确的 `Bearer ` 前缀，`apiKeyMatches` 兼容一次 `sk-` 前缀差异。配置 `api_key` 为空时，本地接口不校验 key。

本地接口见 `NewHandler`：

| 方法与路径 | 用途 | 本地鉴权 |
| --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions 兼容入口 | 配置 key 时启用 |
| `POST /v1/messages` | 经过兼容层转换的 Messages 入口 | 配置 key 时启用 |
| `GET /v1/models` | 模型目录与部分能力元数据 | 配置 key 时启用 |
| `GET /status` | 账号池状态，包含账号标识与冷却原因 | 配置 key 时启用 |
| `GET /healthz` | 可服务性探活，返回 `200` 或 `503` | 无 |
| `GET /prefer`、`POST /prefer` | 读取或修改运行时优先名单 | 配置 key 时启用 |

`/prefer` 写入只在内存生效，重启后重新读取配置文件。`/healthz` 判断的是池中当前是否有可被调度的账号，不会现场执行一次上游生成。

## 2. 站点、realm 与签发域

`internal/loginsite/loginsite.go` 集中定义登录站点：

| 登录站点 ID | realm | 登录 API base | 登录 Origin |
| --- | --- | --- | --- |
| `cn` | `cn` | `https://copilot.tencent.com` | `https://www.codebuddy.cn` |
| `intl` | `global` | `https://www.codebuddy.ai` | 同 API base |
| `workbuddy` | `global` | `https://www.workbuddy.ai` | 同 API base |

`ForRealm("global")` 保留历史默认，选择 `workbuddy` 站；显式 `intl` 才选择 `codebuddy.ai`。不要把“国际 realm”与某一个固定主机名等同。

凭证的 `Auth.Realm()` 见 `internal/auth/auth.go`：启用 global 路由时，存储 realm 为 `global`，或 domain 属于 `codebuddy.ai` / `workbuddy.ai` 及其子域，均判为 global；其余判为 CN。该实现没有让显式 `cn` 覆盖国际 domain。`global.enabled=false` 会在账号和上游两层禁用国际路由；它是运行时路由开关，不会把既有凭证迁移到另一站点。

`internal/upstream/client.go` 的路由优先级为：

| 请求族 | CN 默认 base | global base 选择 |
| --- | --- | --- |
| chat、refresh、模型目录 | `https://copilot.tencent.com` | 显式 `global.chat_base` → 凭证 domain 对应的国际站 → `https://www.workbuddy.ai` |
| billing、签到等 | `https://www.codebuddy.cn` | 显式 `global.billing_base` → 凭证 domain 对应的国际站 → `https://www.workbuddy.ai` |

`loginsite.BaseForDomain` 只识别两类国际后缀。Origin / Referer 另外由 `originRefererFor` 按 realm 与签发域决定；覆盖网络 base 不会自动改写 Origin。这些覆盖项主要用于部署和测试，不是跨域凭证通用性的承诺。

## 3. 浏览器登录与 token refresh

登录入口见 `cmd/login/main.go` 的 `runURLForSite`、`runPollForSite`，站点常量见 `internal/loginsite/loginsite.go`。

1. 向所选站点 `POST /v2/plugin/auth/state?platform=CLI`，JSON body 为 `{}`。
2. 从业务信封的 `data` 取得 `state` 与 `authUrl`；本地临时 state 文件记录 state、realm 和站点。
3. 在浏览器完成返回链接中的授权；`DecorateAuthURL` 补充代码内版本参数。本实现没有 PKCE verifier / challenge。
4. `GET /v2/plugin/auth/token?state=…` 取得 `accessToken`、`refreshToken`、`expiresIn`、`domain`。
5. 带新 access token 请求 `GET /v2/plugin/login/account?state=…`，读取 UID、enterpriseId、nickname。

CLI 的 `poll` 子命令每次只查询一次，不是常驻自动轮询器；外层调用方负责下一次查询。`11217` 与 `12151` 被视为登录尚未完成；`12005`、`11212`、`11216` 是终态许可错误。state 与本次 site / realm 不匹配会被拒绝。其他账号信息查询错误并非全部强制终止：应审查输出中是否有预期账号字段，不能只看 token 是否存在。

若国际站 token 响应缺少 domain，新登录输出以所选站点补齐；已存在凭证的非空 domain 不因此被迁移。`auth.Parse` 接受嵌套与扁平两种 JSON 凭证，并将毫秒级 `expiresAt` 转为秒。账号目录加载仅匹配 `workbuddy*.json`。

刷新见 `Client.RefreshToken`：

```text
POST {chatBase}/v2/plugin/auth/token/refresh
X-Refresh-Token: <该账号 refresh token>
```

刷新请求还使用公共头及刷新来源标识；chat 请求不会携带 `X-Refresh-Token`。刷新单次 I/O 限时 30 秒；网络调用不长期持有账号互斥锁。成功后更新 access token，响应缺失的 refresh token、domain、expiry 保留旧值。调用方负责 `SaveAtomic` 持久化，保存失败时内存中的新 token 仍可能已经生效。handler 默认在距过期时间 10 分钟内先刷新。

`cmd/login poll` 的正常输出就是完整凭证 JSON，因此其标准输出、临时 state 文件和账号文件都属于私密数据，不能放入公开仓库或排障示例。

## 4. 模型名与上游路径

`internal/server/resolve_model.go` 的 `ResolveModel` 解析 `[realm:]model`：

| 本地输入 | 选取 realm | 发往上游的 model |
| --- | --- | --- |
| `cn:example-model` | `cn` | `example-model` |
| `global:example-model` | `global` | `example-model` |
| `example-model` | `cn` | 原值 |
| 其他前缀，包括大写前缀 | `cn` | 原值 |

只识别第一个冒号前精确小写的 `cn` / `global`。前缀属于本地路由协议，不是上游模型 ID。

上游 chat 始终 `POST`，见 `ChatStreamContext`、`chatPaths`：

| realm | 路径 | 路径回退 |
| --- | --- | --- |
| CN | `/v2/chat/completions` | 无 |
| global | `/console/chat/completions` | 仅首次 HTTP `404` / `405` 时，在同一 base 重试 `/v2/chat/completions` |

路径回退不改变 realm 或签发域，也不因任意业务拒绝切换路径。

chat 使用独立 HTTP client，不设整体生成时长上限；配置层默认响应头超时约 120 秒，流中连续空闲约 300 秒时取消读取。它们分别限制等待响应头和数据停滞，不能理解为模型思考最长只能持续 120 秒。handler 传递入站 context，客户端取消会向上游传播。短 RPC 默认整体约 120 秒，refresh 另设 30 秒限制。

模型发现有两层：`FetchModels` 优先 `GET /v3/config`，失败回落 CN 的 `/console/enterprises/personal/models` 或 global 的 `/v2/enterprises/personal/models`；`FetchGlobalModels` 还维护国际模型静态集合与远端结果的并集。目录成功缓存约一小时，失败负缓存约五分钟。`/v1/models` 可以返回静态兜底条目，即使当前没有对应可用账号，因此“目录中可见”不代表该账号具有调用权限。

本地模型条目可能包含 `context_length`、`max_output_tokens`、`supports_images`、`tier`、`reasoning_supported_efforts`、`reasoning_default_effort`。它们是能力描述或兜底信息，不是 handler 对请求预算、订阅权限或多模态内容的完整验证。

## 5. 上游请求头的来源

实现位于 `internal/upstream/headers.go`。以下是当前代码发送字段的分类，**不能据此宣称每个字段都是服务器强制要求**。本次整理没有对单个头做在线必要性实验，源码注释中的“必带”“风控”推断不等于公开协议保证。

| 类别 | 字段及来源 |
| --- | --- |
| HTTP 编码与流类型 | `Content-Type: application/json`；chat 的 `Accept` 包含 JSON 和 event-stream；普通 RPC 接受 JSON |
| 账号鉴权 | chat / billing 的 Bearer 值来自账号 access token；refresh 的 `X-Refresh-Token` 只来自该账号刷新凭证 |
| 账号身份与组织 | `X-User-Id` 来自 UID；CN 企业与 domain 取自凭证，缺失时设置相应 `X-No-*` 标记；global chat 声明无企业，并按签发站点填写 domain |
| 站点 | Origin / Referer 来自 realm 和已识别的国际签发域；Accept-Language 按 CN / global 选取 |
| 会话和追踪 | `X-Conversation-ID`、`X-Conversation-Request-ID`、`X-Conversation-Message-ID`、`X-Request-ID`、`X-Root-Request-ID`、`X-Trace-ID` 和 B3 头由请求元数据与本地 ID 生成逻辑组成 |
| 可选设备数据 | `X-Device-Token` 的来源优先级为账号字段 → 配置值 → 配置文件读取；全部缺失则不发送，chat / billing 使用，公共 refresh / 模型目录头不使用 |
| 可选客户端 IP | 仅开启 `passthrough_ip` 时，从入站 X-Forwarded-For 首段或 X-Real-IP 取值；默认关闭 |
| 现有客户端默认值 | User-Agent、产品 / IDE 归属头、`X-CodeBuddy-Request`、`X-Requested-With` 等由代码默认和配置组成；部分是客户端标识兼容逻辑，不是 OAuth 授权证明 |
| 本地合成标识 | `X-Machine-ID` / `X-Session-ID` 在 UID 非空时由本地稳定派生；它们并非设备 SDK 发出的凭证，不能证明真实官方客户端或真实设备 |

本说明不提供客户端身份冒充、指纹清洗规则或规避上游校验的配置配方。源码已有相关历史逻辑属于需要审查的实现行为，不能把其默认值当成官方授权或稳定访问承诺。

`ResolveConversationID` 从 JSON body 的 metadata 或顶层 conversation 字段取值；该版本不从入站 `X-Conversation-ID` 读取它。值为空时，`injectConversationHeaders` 实际不发送 `X-Conversation-ID`，尽管该文件附近一段历史注释称会恒发兜底。

入站 `X-Conversation-Request-ID` 优先透传；没有时，存在会话键则派生进程内稳定 ID，否则按最后一条 user 消息的位置和文本派生轮级 ID；没有可提取的文本则生成随机 ID。同一次 handler 内的重试共用聚合 ID，而每次出站产生新的消息 ID。请求 ID 表示追踪关系，不证明该请求由某一客户端品牌发出。

## 6. 请求体适配

主入口：`Client.prepareBody` → `PrepareBodyOptWithEffortsAndDefault` → `InjectPromptCacheKey`，见 `internal/upstream/client.go`、`payload.go`、`thinking.go`、`cache_key.go`。

1. 强制出站 `stream:true`。调用方的 `stream:false` 通过本地聚合实现，不会让上游收到非流式请求。
2. 缺少 `stream_options` 时补 `include_usage:true`；客户端已经提供该键时不覆盖，故显式关闭 usage 可能使扣费统计缺失。
3. `developer` role 归一为 `system`；该行为独立于内容改写开关。源码历史注释记录过不支持角色也返回 `11128` 的情形。
4. `tool_choice` 对象适配为字符串：auto / required 保留其字符串语义，指定函数对象转为函数名；none 删除 tool_choice 及 tools / functions；无法识别的对象或非标量被删除。此处不是 OpenAI 各版本工具语义的完全等价实现。
5. `cleanupOrphanToolCalls` 处理工具调用和结果的配对，可能删除历史中的 tool_calls 或 tool 消息。它按整个消息列表中的 ID 集合处理，没有完整验证先后顺序与批次。
6. DeepSeek 模型默认补 `thinking.type=enabled` 及模型声明的默认 effort；缺元数据时回落 `high`。显式 thinking disabled，或 effort 为 off / none / disabled 时关闭思考并删除 effort 字段。非 DeepSeek 不执行这项 thinking 注入。
7. 已知模型的 effort 按该 realm 能力集合调整；能力缺失有静态兜底或透传分支。若全部支持档位高于请求档位，实际会选最低支持档，可能提高思考开销，不能一概称为“只降级”。
8. DeepSeek 历史出现 reasoning 痕迹时，为缺失字段的 assistant 消息补 `reasoning_content`，已有字段保留。第一遍痕迹检测实际遍历全部角色，并非只检查 assistant。
9. 代码中还保留了可配置的消息文本清洗，以及提示词替换和拒绝后的重试逻辑。它们会改变消息内容，不属于无损协议适配；本说明不复述匹配表和替换配方。
10. global 请求在上述处理后，若消息非空且第一条不是 system，会补一个简短 system。这同样会改变请求消息列表。

本快照**没有**将 `max_completion_tokens` 自动转换为 `max_tokens`；预算别名与许多未知字段仍原样传出。入站 JSON 也没有完整 schema 验证，解析失败时多处改写函数选择原样返回，最终错误可能由上游产生。

`prompt.mode` 默认 passthrough，但不能据此声称所有消息逐字透传：其他适配仍生效，既有拒绝降级状态还会替换 system / developer，影响后续请求直到当地次日零点。custom 模式则直接用配置提示词替换全部 system / developer。见 `internal/prompt/prompt.go`、`internal/server/degrade.go` 与 `chatCompletions`。

### 缓存键与会话粘性

自动 prompt cache key 由账号 UID 和会话标识派生；body 的非空 `prompt_cache_key` 优先保留，随后优先读取顶层 conversation_id / conversationId，再使用 handler 解析的会话值。它是上游前缀缓存的分组键，不是本地对话内容缓存，也不保证命中或固定价格。

会话值为空时，同一账号实际得到稳定的相同 key；源码关于“空会话不复用”的注释与实现不符。显式客户端 key 不会再由网关加上账号隔离段，不能声称所有情况都绝对防止跨账号 key 重合。自动键中也含有 UID 的短前缀，应视为有账号关联性的数据。

本地 session router 是另外一层：从 metadata / 顶层会话字段取得会话键，部分 metadata user_id 也可作为回落，再将它绑定到可用账号。绑定有 TTL，并按 realm 与模型可用性复核；账号不可用时可以换号。因此 prompt cache、追踪 ID 和账号粘性是三个不同机制，不能互相当作成功或隔离证明。

## 7. SSE 与非流式返回

`internal/upstream/sse.go` 的 `Stream` 逐行读取 SSE，将 JSON 数据帧按字段白名单重建并 flush。保留内容、reasoning_content、工具调用、finish_reason 与 usage 等，剔除部分空占位和未知字段；同一工具 index 仅首片保留函数 name，后续参数继续分片。上游未提供终止标记时会补 `[DONE]`；零有效 JSON 帧时发送一条错误帧后结束。

`Aggregate` 将 delta.content 与 reasoning_content 拼接，按工具 index 合并参数片段，把最后观测的 usage 放入单个 `chat.completion` JSON。零有效事件返回解析错误，handler 映射为 `502 upstream_parse`。因长度截断且参数不是完整 JSON 的工具调用可能被删除，见 `truncation.go`。

当前 SSE 处理面向每行 `data: ` 的 OpenAI 常见形态，不是完整通用 SSE 解析器；非流式聚合只有一个返回 choice，不支持独立聚合多候选。任意可解析 JSON 都会计入“有效事件”，所以上游 `200` 的业务错误 JSON 仍可能被当成正常流帧，未知错误字段也可能在规范化中丢失。发生中途传输错误时，已开始的 HTTP 流不能再改成另一状态码。

usage 的 `credit` 是本次消耗观测，不能当剩余额度。handler 在 credit 与 token 总数都存在时更新账号 / 模型成本账本；有 usage 但缺 credit 不会被认定为免费。模型目录中促销声明与真实 usage 扣费是不同来源，本说明不继承代码注释中的固定节省倍数承诺。

### `/v1/messages` 本地兼容层

`lib/anthropiccompat/request.go` 与 `handler.go` 将有限的 Messages 格式转换为既有 Chat 请求，再把 Chat 响应转换回 Messages；没有第二套账号池或直接调用 Anthropic 上游的链路。该模块只挂载在 `/v1/messages`。

- 接受 Bearer 或 `x-api-key`，二者同时非空但不同则拒绝；转换后仍经过 WorkBuddy 的本地鉴权、选号和重试。
- 要求 model、非空 messages 与整数 max_tokens（1 到 131072）。该入口固定原始 / 转换后 body 上限均为 8 MiB，与 Chat 的可配置大小上限并非完全相同。
- 支持文本、客户端 `tool_use` / `tool_result`、工具 schema 与部分 tool_choice 转换；对历史工具 ID、结果配对和响应参数做额外校验。system 和内联 system / developer 会合并到一条 system 消息，顺序表示方式发生变化。
- 不支持图片 / 视频内容块、托管工具、thinking / redacted_thinking 历史，以及结构化 output format。非 disabled 的显式 thinking 请求会被拒绝；请求中的 disabled 本身没有继续写入 Chat body，因此不能据此保证 DeepSeek 上游思考也关闭。响应的 reasoning_content 也不会转成签名 thinking block。
- 流式输出 `message_start`、content block 事件、`message_delta` 与 `message_stop`，对外不使用 Chat 的 `[DONE]` 格式。要求可见 finish_reason 与有效内容，缺失时发兼容层错误；输入缓存 token 从 Chat usage 映射为 cache_read_input_tokens。

该入口是当前实现覆盖范围内的兼容接口，不能标成完整 Anthropic 协议实现。独立模块测试应在 `lib/anthropiccompat` 下另行执行；根模块的 `go test ./...` 不会递归进入嵌套 Go 模块。

## 8. 错误分类、重试与已知缺陷

`internal/upstream/client.go` 的 `Classify` 结合 HTTP 状态、业务码和消息子串进行启发式分类，`internal/server/handler.go` 的 `applyErrorPolicy` 执行池状态变化。以下描述是当前策略，不是上游对账号状态的权威结论：

| 观测 | 当前处理 |
| --- | --- |
| `11102` + 模型不存在语义 | 对该账号 / 模型写负缓存，轮换 |
| `6020` / `6021` | 返回 `503 upstream_busy`，不处罚账号 |
| `402` 或余额关键词 | 账号冷却到次日 04:00 |
| `429` 或限流关键词 | 软冷却；带重置时间的 `6004` 可按模型独立冷却 |
| `404` | 短冷却；global 路径回退先在上游层处理 |
| `5xx` | 连续错误计数与熔断 |
| `12153` / Offline session 字样 | chat 错误路径仍会立即禁用；refresh / keepalive 的部分路径使用连续计数 |
| `request illegal` 等账号故障匹配 | 既有策略可能直接禁用；这是本地推断，不能单凭该文案断言永久封号 |
| 无业务信封的 `401` | 先尝试 refresh，按确定性与否处理，然后冷却 / 轮换，不直接等同封号 |
| 请求解析错误，如 `11101` | 不处罚账号，但仍可能轮换；末端返回通常仍为 `503`，错误语义不够准确 |

默认一次 handler 最多经历三次选号 / 重试机会。传输错误不记账号熔断，但仍可能产生额外出站尝试；不应将这种轮换当作绕过上游明确拒绝的方法。

必须保留以下排障边界：

- **`11128` 不能单独证明 DSH 渠道限制、内容违规、密钥无效或封号。** 本快照会按部分“unapproved channel / illegal invocation”文案归为 content_blocked，并可能替换系统消息重试；其给客户端的“违禁词 / 内容防火墙”解释超出了观测证据。角色、请求结构与上游规则都应分别核对。
- chat 的 `12153` 立即禁用与 refresh / keepalive 的连续失败阈值不一致；老状态文件还可能保留历史误判的 disabled 记录。token 后来能刷新不保证池自动复活，积分查询成功也不证明 chat 可用。
- handler 收到 HTTP 成功后、读取完整 SSE 之前就记 `NoteSuccess`；流式分支还忽略 `Stream` 的返回错误。因此成功计数、healthz 和 HTTP 200 都不足以证明一次完整生成成功。
- 错误文本通过子串识别，广泛的限流词或业务码匹配可能把请求级问题计为账号健康问题。代码保留“本地禁用”状态不意味着平台已经正式封禁该账号。
- 工具配对清理按全局 ID 集合运行：一批调用只有部分结果时可能删除整批 tool_calls，却保留已经匹配的某条结果，留下新的不完整历史；该机制并非严格正确的工具历史修复。
- 最终错误会隐去许多原始上游细节；某些客户端还会把任意 `403` 统一显示为鉴权错误。排障应关联本地状态码、网关错误码与经过脱敏的结构性证据，不能仅按客户端标签归因。

这些问题在本次源码整理中没有被暗中修复，也没有执行真实账号探测。后续修复应分别补离线契约测试与实际客户端端到端验证，再更新行为说明。

## 9. 阅读入口与离线验证

本快照附带 `scripts/` 的五份辅助脚本。它们与 Go 凭据加载器并非完全等价：Python `task_common.py` 使用嵌套 account/auth 结构和较窄的 `workbuddy-*.json` 文件匹配，旧国际域识别也未完整覆盖 Go 的 codebuddy.ai 签发域逻辑。不要用脚本读取结果反推 Go 网关的完整站点支持。脚本需 Python 3.10 或以上；可选 Linux cron 入口另需 Bash / flock。本次只校验源码与 Python 语法，未执行这些账号任务。

| 关注点 | 源码入口 |
| --- | --- |
| 凭证解析、realm、原子保存 | `internal/auth/auth.go` |
| 站点表与授权状态码 | `internal/loginsite/loginsite.go` |
| 登录流程 | `cmd/login/main.go` |
| 本地 API、选号、错误出口 | `internal/server/handler.go` |
| 模型前缀 | `internal/server/resolve_model.go` |
| 路由、refresh、chat、目录 | `internal/upstream/client.go` |
| 请求头 | `internal/upstream/headers.go` |
| body、thinking、缓存键 | `internal/upstream/payload.go`、`thinking.go`、`cache_key.go` |
| 工具历史与截断 | `internal/upstream/tool_pairing.go`、`truncation.go` |
| SSE 与非流式聚合 | `internal/upstream/sse.go` |
| Messages 请求 / 响应适配 | `lib/anthropiccompat/request.go`、`handler.go` |
| 网关 `401` 恢复 | `internal/upstream/gateway_auth.go`、`internal/server/gateway_auth.go` |
| 会话键与追踪 ID | `internal/session/session.go`、`ids.go` |
| 账号池状态机 | `internal/pool/state.go`、`transition.go`、`cooldown.go` |

仓库测试大多使用本地 HTTP stub 或自定义 RoundTripper，不需要把账号文件提供给测试。优先运行 `go test ./...` 和 `go vet ./...`。测试通过只能证明覆盖到的本地实现，不代表未公开上游接口或某个真实客户端已恢复。
