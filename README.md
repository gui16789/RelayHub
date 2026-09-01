# RelayHub

**本地 LLM 代理桌面管理平台**

一个轻量级、原生桌面化的多渠道 LLM 转发管理面板。
支持 OpenAI、Anthropic、Gemini 等多个 AI 渠道，提供图形化界面管理和流量监控。

---

## 特性

- 纯 Go + Wails 原生桌面应用（另有无头服务模式）
- 多渠道 LLM 转发：OpenAI 兼容原样透传，Anthropic / Gemini 自动做格式转换（含流式）
- 支持端点：`/v1/chat/completions`、`/v1/responses`、`/v1/embeddings`、`/v1/images/*`、`/v1/audio/*`、`/v1/moderations`、`/v1/models`
- 密钥轮询、失败自动降级、429/5xx 冷却、Retry-After 感知
- 后台健康探针（并发探测，故障通道自动摘除/恢复）
- 实时流量统计与请求路由追踪（统计计数持久化到 stats.json，重启不丢）
- 管理控制台鉴权：默认仅本机回环可访问，可配置 `admin_key` 开放远程管理
- API Key 支持 `${ENV_VAR}` 环境变量引用，避免明文落盘
- 中文界面，双击启动 + 端口占用自动保护
- 配置文件热加载（手改 config.yaml 约 0.5s 内生效，无需重启）
- 结构化日志落盘：滚动文件（10 MiB × 3，14 天），桌面模式日志不再丢失，默认位于 `%APPDATA%\RelayHub\logs\`

## 快速开始

### 1. 安装与运行

```bash
# 克隆项目
git clone <repository-url>
cd proxy

# 桌面版
go run main.go

# 无头服务模式（无 GUI，支持 Ctrl+C 优雅退出）
go run ./cmd/headless

# 或者直接双击构建好的 RelayHub.exe（见 build/ 目录）
```

### 2. 访问管理面板

默认代理服务监听端口为 `8787`，管理控制台地址为：

**http://localhost:8787/admin/**

> 注意：管理台默认只允许本机（127.0.0.1）访问。如需远程管理，
> 请在 config.yaml 设置 `server.admin_key`，远程请求携带
> `Authorization: Bearer <admin_key>` 方可访问。

---

## 配置详解

完整注释示例见 [config.example.yaml](config.example.yaml)。

```yaml
server:
  listen: ":8787"            # 代理服务监听地址
  api_key: ""                # 可选，客户端 Bearer 密钥（为空则不校验）
  admin_key: ""              # 可选，管理台远程访问密钥（为空则仅本机）
  max_attempts: 0            # 单请求最大尝试次数（渠道×密钥），0 = 不限
  enabled: true

channels:
  - name: "OpenAI"
    type: openai             # openai | anthropic | gemini
    base_url: "https://api.openai.com"
    api_keys: ["${OPENAI_KEY}"]   # 支持环境变量引用
    models: ["gpt-4o", "gpt-*"]   # 支持尾部通配符
    model_map:                   # 可选，客户端模型名 -> 上游模型名
      gpt-4o: "gpt-4o-2024-08-06"
    headers:                     # 可选，附加请求头（保留头不可覆盖）
      X-Title: "my-proxy"
    priority: 10                 # 越大越优先，失败自动降级
    enabled: true
```

### 配置字段说明

- `server.listen`：代理服务监听地址（支持 `0.0.0.0:xxxx`；暴露到公网时务必配置 `api_key` 与 `admin_key`）
- `server.api_key`：客户端 Bearer 密钥（为空则不启用，恒定时间比较防时序攻击）
- `server.admin_key`：管理台远程访问密钥；**为空时 /admin/ 仅回环地址可访问**
- `server.max_attempts`：单请求最多尝试的上游组合数，防止渠道过多时请求拖太久
- `channels` 数组：
  - `type`：渠道协议（`openai` 原样透传；`anthropic` / `gemini` 自动格式转换，仅适用于 chat）
  - `name`：通道名称（显示在面板上）
  - `base_url`：基础 URL（结尾的 `/v1` 会被自动去除）
  - `api_keys`：密钥列表，轮询使用，支持 `${ENV_VAR}`
  - `models`：模型列表，支持 `claude-*` 形式的尾部通配符
  - `model_map`：客户端模型名到上游模型名的映射
  - `headers`：附加请求头（`Authorization`、`Content-Type` 为保留头）
  - `priority`：优先级，数字越大越优先

---

## 支持的渠道

| 渠道       | 类型      | 说明                                       |
|------------|-----------|--------------------------------------------|
| OpenAI     | openai    | OpenAI 兼容站点，所有 /v1/* 端点原样透传   |
| Anthropic  | anthropic | 自动转换（仅 /v1/chat/completions）        |
| Gemini     | gemini    | 自动转换（仅 /v1/chat/completions）        |

---

## 项目结构

```
proxy/
├── main.go                 # Wails 桌面入口
├── cmd/
│   ├── headless/           # 无头服务模式（优雅退出）
│   └── mockup/             # 测试辅助入口
├── internal/
│   ├── server/             # HTTP 路由、鉴权中间件、stats 持久化
│   ├── relay/              # 核心代理转发（OpenAI/Anthropic/Gemini、冷却、探针）
│   ├── admin/              # 管理控制台（内嵌 HTML）与模型探针
│   ├── config/             # 配置解析、校验、环境变量展开
│   ├── store/              # 配置文件热加载与持久化
│   └── stats/              # 流量统计（计数持久化到 stats.json）
├── frontend/               # Wails 启动页（dist/index.html）
└── config.yaml             # 本地配置（已在 .gitignore 中，勿提交）
```

---

## 开发与构建

### 开发环境

```bash
go version
# 推荐 Go 1.21+
```

### 测试

```bash
go test ./...
```

### 构建桌面应用

```bash
# Wails 官方方式（推荐，产物在 build/bin/，带图标）
wails build

# 或手动 go build（必须带 Wails 的生产标签，否则窗口无法启动）
go build -tags desktop,production -ldflags "-w -s -H windowsgui" -o RelayHub.exe .
```

构建后可在 `build/` 目录找到 `RelayHub.exe`

---

## 贡献指南

欢迎提交 PR！

1. 提交清晰的 commit message
2. 保持代码风格一致（Go 标准 + 中文注释）
3. 新功能请先创建 issue 讨论

---

## 版本历史

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 与[语义化版本](https://semver.org/lang/zh-CN/)。

---

## 许可证

MIT License - 详见 [LICENSE](LICENSE) 文件

---

## 联系方式

如果有任何问题或建议，欢迎通过以下方式联系：

- GitHub Issues
- 提交 Pull Request
