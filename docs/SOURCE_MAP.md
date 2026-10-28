# 协议源码导航

阅读顺序建议：登录与凭据 → 域名与请求头 → 模型目录 → 请求转换 → SSE → 错误与账号状态。

| 主题 | 实现入口 |
| --- | --- |
| 登录 CLI、设备授权流程 | [cmd/login/main.go](../cmd/login/main.go)、[legacy_flow.go](../cmd/login/legacy_flow.go) |
| CN / Global 登录网站 | [internal/loginsite/loginsite.go](../internal/loginsite/loginsite.go) |
| 凭据结构、有效期、落盘 | [internal/auth/auth.go](../internal/auth/auth.go) |
| 上游 HTTP、刷新与聊天路径 | [internal/upstream/client.go](../internal/upstream/client.go) |
| 公共请求头、会话头、版本默认值 | [internal/upstream/headers.go](../internal/upstream/headers.go) |
| 网关认证恢复 | [internal/upstream/gateway_auth.go](../internal/upstream/gateway_auth.go)、[internal/server/gateway_auth.go](../internal/server/gateway_auth.go) |
| 设备 Token 的读取接口 | [internal/upstream/device_token.go](../internal/upstream/device_token.go) |
| 模型目录与元数据 | [internal/upstream/directory_v3.go](../internal/upstream/directory_v3.go)、[global_models.go](../internal/upstream/global_models.go) |
| 思考档位目录 | [internal/upstream/effort_catalog.go](../internal/upstream/effort_catalog.go) |
| 模型名前缀解析 | [internal/server/resolve_model.go](../internal/server/resolve_model.go) |
| 请求体转换 | [internal/upstream/payload.go](../internal/upstream/payload.go) |
| 工具调用和结果配对 | [internal/upstream/tool_pairing.go](../internal/upstream/tool_pairing.go) |
| DeepSeek thinking / reasoning | [internal/upstream/thinking.go](../internal/upstream/thinking.go) |
| 提示词缓存键 | [internal/upstream/cache_key.go](../internal/upstream/cache_key.go) |
| SSE 解码、流式透传与非流式聚合 | [internal/upstream/sse.go](../internal/upstream/sse.go) |
| 本地 HTTP 接口、鉴权、重试和错误返回 | [internal/server/handler.go](../internal/server/handler.go) |
| Anthropic Messages 兼容转换 | [lib/anthropiccompat/request.go](../lib/anthropiccompat/request.go)、[handler.go](../lib/anthropiccompat/handler.go) |
| 账号选择、并发与状态 | [internal/pool](../internal/pool) |
| 会话绑定 | [internal/session](../internal/session) |
| 配置与环境变量 | [cmd/server/config.go](../cmd/server/config.go)、[wiring.go](../cmd/server/wiring.go) |
| 定时任务与共享排程 | [internal/scheduler](../internal/scheduler)、[internal/config/schedule.go](../internal/config/schedule.go) |

每个目录中的 `*_test.go` 保留了协议边界及兼容案例。测试身份占位不代表可用的账号或凭据。
