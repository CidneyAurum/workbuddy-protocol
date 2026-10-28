# WorkBuddy 协议与本机反代源码

本仓库保存 2026-10-28 从本机 WorkBuddy 反代整理的 Go 源码快照，重点记录登录、鉴权、模型目录、请求转换和流式响应协议。它是当前本机代码的独立副本，不是重新下载的上游版本，也不是 WorkBuddy 官方 SDK。

## 从这里阅读

- [协议说明](PROTOCOL.md)：CN / Global 路由、OAuth、令牌刷新、请求头、模型和聊天协议、SSE、错误处理及已知局限。
- [源码导航](docs/SOURCE_MAP.md)：各协议对应的实现入口。
- [来源与归属](NOTICE.md)：原始项目、本地修改和许可文件来源。
- [原有 README](docs/UPSTREAM_README.md)：保留本机源码附带的项目说明、归属和声明；Key 示例统一使用导出占位符。
- [源码快照清单](SOURCE_MANIFEST.json)：源文件摘要、导出调整及校验结果。

## 仓库内容

```text
cmd/                     网关及登录、刷新、模型目录等命令
internal/upstream/       上游 HTTP、模型、请求体、SSE 和错误分类
internal/loginsite/      登录网站及域名选择
internal/auth/           凭据结构、有效期和原子存储
internal/server/         本地接口、鉴权及响应聚合
internal/pool/           账号选择、并发、冷却和持久化
internal/session/        会话标识与账号绑定
lib/anthropiccompat/     本机 Anthropic Messages 兼容模块
scripts/                 定时任务与地区辅助脚本的源码
docs/                    协议、源码导航和原说明
config.example.json      新建的占位配置，不含本机配置值
```

只收录源码、测试和文档。账号文件、访问令牌、刷新令牌、真实网关 Key、运行配置、日志、会话、状态数据库及可执行程序均不在仓库内。

## 构建和测试

模块声明 Go 1.22.5；本次整理使用 Go 1.27.0 验证，未另行验证最低版本兼容性。

```powershell
go test ./...
go vet ./...
go build ./...

# 兼容层是独立 Go module，需要单独测试。
Push-Location lib/anthropiccompat
go test ./...
Pop-Location

# 仅构建网关程序。
go build -trimpath -o bin/wb2api.exe ./cmd/server
```

原来的外部本机依赖 `../../anthropiccompat` 已改为本仓库内的 `./lib/anthropiccompat`，无需整个本地工作台目录即可编译。

`scripts/` 中的 Python 辅助任务需要 Python 3.10 或更高版本，仅使用标准库；可选的 Linux cron 入口还需要 Bash 与 `flock`。这些脚本不参与 Go 编译，示例配置默认关闭相应定时任务。

## 本地配置

在仓库的独立目录中使用示例配置；示例只监听回环地址，六类定时任务均关闭。配置字段和环境变量覆盖规则见 [cmd/server/config.go](cmd/server/config.go)。

```powershell
Copy-Item config.example.json config.json
# 在 config.json 中自行设置 api_key，或用 WB2A_API_KEY 环境变量覆盖。
go run ./cmd/server -config config.json
```

首次启动没有账号时，不具备生成能力。登录命令实现见 [cmd/login](cmd/login/main.go)，凭据结构见 [internal/auth/auth.go](internal/auth/auth.go)。此仓库不提供预置账号或令牌。

## 快照边界

此次整理不改变本机正在运行的网关。保留原有产品实现；导出时调整本地模块路径、将少量测试身份数据和文档 Key 示例替换为合成值，并修正一组测试的本地 HTTP 隔离。具体文件和调整记录在快照清单中。

测试通过只表明这份源码可编译且离线测试通过，不代表当前上游仍接受全部协议，也不代表之前 DSH 的完整 Agent 调用已恢复。协议说明中的已知问题按实际代码记录，不将 `11128` 单独解释为特定客户端被禁止。
