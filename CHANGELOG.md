# Changelog

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与[语义化版本](https://semver.org/lang/zh-CN/)。

## [Unreleased]

### Added
- 服务器部署支持：多阶段 `Dockerfile`（非 root、健康检查、`/data` 卷持久化）、`docker-compose.yml` 与部署文档 `docs/deploy-server.md`（含 Caddy / Nginx HTTPS 反代示例与安全清单）
- 首次启动网页引导：配置文件缺失时以默认配置启动，`/admin/setup` 提供初始化向导（设置 admin_key / api_key / 第一个渠道），仅当 admin_key 未设置时对远程开放，初始化完成后自动关闭并回到常规鉴权

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