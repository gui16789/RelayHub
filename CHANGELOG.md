# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Security
- 配额冷却持久化不再写入明文密钥：`cooldowns.json` 现在只存密钥的单向指纹（截断 SHA-256）与尾 4 位，与项目其他位置「只记尾号」的做法一致。密钥在包内的身份标识本身改为指纹，因此重启恢复无需明文。旧版本写下的明文条目在首次加载时自动迁移为指纹并立即重写文件（不迁移会让所有已耗尽的密钥在升级时同时苏醒）

### Added
- 服务器部署支持：多阶段 `Dockerfile`（非 root、健康检查、`/data` 卷持久化）、`docker-compose.yml` 与部署文档 `docs/deploy-server.md`（含 Caddy / Nginx HTTPS 反代示例与安全清单）
- 首次启动网页引导：配置文件缺失时以默认配置启动，`/admin/setup` 提供初始化向导（设置 admin_key / api_key / 第一个渠道），仅当 admin_key 未设置时对远程开放，初始化完成后自动关闭并回到常规鉴权
- CI 流水线 `.github/workflows/ci.yml`：push / PR 上跑 `go vet`、`gofmt`、`go test -race`、`go mod verify`，并交叉编译全部发布目标 + 构建 Docker 镜像（此前只有打 tag 时的 release 工作流，测试从不在合并前运行）

### Fixed
- **`.gitignore` 的 `go.*` 规则把 `go.mod` / `go.sum` 一起排除了**，导致全新 clone 不是一个 Go module：什么都编译不了，`Dockerfile` 的 `COPY go.mod go.sum` 失败，release 工作流全线挂掉。规则收窄为 `go-build*`，两个文件重新纳入版本控制
- **桌面版在 macOS / Linux 上编译失败**：`main.go` 无条件导入 `golang.org/x/sys/windows`。平台相关的错误对话框拆分为 `fatal_windows.go` / `fatal_other.go`，非 Windows 平台写 stderr
- 无头版 Windows 二进制不再带 `-H windowsgui`：该链接选项会剥离控制台，日志看不见、Ctrl+C 收不到，而这正是无头模式的关停路径
- Anthropic / Gemini 转发改用 `NewRequestWithContext` 传递客户端上下文：此前客户端断开后，上游流会继续生成（`streamClient` 无总超时）并继续计费
- Gemini 不再套用 Anthropic 的 `max_tokens` 默认值（4096）：Gemini 本身不要求该字段且默认用模型上限，之前会让未设上限的请求被静默截断
- 工具链版本统一：`go.mod` 声明 `go 1.26`，CI 改用 `go-version-file: go.mod` 读取，消除此前 go.mod 要求 1.26 而 CI 装 1.24 的分叉
- `cooldowns.json` 移出版本控制并加入 `.gitignore`（该文件会写入上游 API Key）
- 渠道重名校验：`config.Load` 拒绝重名渠道，并拒绝仅大小写不同的名字。渠道名是冷却状态、5xx 连击计数、探针健康与 per-channel 统计的 map key，重名会让两个渠道静默共享全部状态。管理台的新增/重命名路径同步改为大小写不敏感判断 —— 此前可以从 UI 存进一份下次启动会被加载器拒绝的配置，导致服务无法用自己保存的状态启动
- Release 工作流桌面版构建：`go install wails` 之后把 `GOPATH/bin` 加入 PATH（hosted runner 默认 PATH 里没有），并把版本钉在 v2.15.0 与 go.mod 一致（此前用浮动的 @latest）。修复 darwin/amd64 desktop 构建失败 "wails: command not found"


### Changed
- 转换渠道遇到无法表达的特性（`tools` / `tool_choice`、多模态 `image_url` 等 content part、`response_format`、`n>1`）时不再静默丢弃后返回 200。这类请求被限制路由到 `openai` 类型渠道（原样透传）；若该模型没有 `openai` 渠道，返回 400 并点名具体特性与解决办法
- 桌面版关闭窗口时先排空在途请求（10 秒 `Shutdown`）再停后台任务，与无头版的关停语义对齐；此前正在流式输出的请求会被硬切
- GitHub Actions 升级：`checkout` v4→v7、`setup-go` v5→v7、`setup-node` v4→v7、`upload-artifact` v4→v7、`download-artifact` v4→v8，消除 Node 20 弃用告警

## [1.0.1] - 2025-09-01

### Added
- 进程级结构化日志落盘：`slog` 输出同时写入 stderr 与滚动日志文件（单文件 10 MiB、保留 3 个、14 天、压缩归档），桌面 GUI 模式不再丢失日志
- `config.yaml` 新增可选 `logging:` 段（`level` / `dir` / `file`），缺省时日志写到 `%APPDATA%\RelayHub\logs\`
- 桌面版与无头版使用独立日志文件（`proxy.log` / `proxy-headless.log`），避免两个进程共享文件句柄

## [1.0.0] - 2025-01-01

### Added
- 多渠道 LLM 转发：OpenAI 兼容原样透传，Anthropic / Gemini 自动格式转换（含流式）
- 支持端点：`/v1/chat/completions`、`/v1/responses`、`/v1/embeddings`、`/v1/images/*`、`/v1/audio/*`、`/v1/moderations`、`/v1/models`
- 密钥轮询、失败自动降级、429/5xx 冷却、Retry-After 感知（上限 10 分钟）
- 后台健康探针：并发探测，故障通道自动摘除/恢复
- 实时流量统计与请求路由追踪，计数持久化到 `stats.json`，重启不丢
- 管理控制台：内嵌 HTML，支持渠道增删改、模型映射、总开关，改动自动写回 `config.yaml`
- 配置文件热加载：手改 `config.yaml` 约 0.5 秒内生效，解析失败时保留运行中配置
- API Key 支持 `${ENV_VAR}` 环境变量引用
- Wails 桌面版 + `cmd/headless` 无头服务模式（Ctrl+C 优雅退出）
- 端口占用保护：先绑定端口再开窗口，避免连到旧实例

### Security
- 管理台默认仅本机回环（127.0.0.1/::1）可访问，远程需配置 `admin_key`（Bearer 鉴权）
- 客户端 `api_key` 使用恒定时间比较，防时序攻击
- 日志与错误信息只记录 API Key 尾部 4 位
- `config.yaml` / `stats.json` 以 0600 权限写入，临时文件 + rename 原子替换

[Unreleased]: https://github.com/local/relayhub/compare/v1.0.1...HEAD
[1.0.1]: https://github.com/local/relayhub/releases/tag/v1.0.1
[1.0.0]: https://github.com/local/relayhub/releases/tag/v1.0.0