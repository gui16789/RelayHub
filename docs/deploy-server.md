# 服务器部署指南（Docker + HTTPS 反代）

RelayHub 的无头服务模式（`cmd/headless`）专为服务器设计：无 GUI、
优雅退出、浏览器打开 `/admin/` 即可在网页上管理渠道（base_url、
api_keys、模型、优先级等），改动自动写回 config.yaml 并热生效。

最终效果：

- 客户端（NextChat / Cherry Studio / 自研程序等）填：
  - **Base URL**：`https://proxy.example.com/v1`
  - **API Key**：`server.api_key` 的值
- 管理者浏览器打开：`https://proxy.example.com/admin/`（Bearer
  携带 `server.admin_key`）做所有网页设置。

---

## 1. 准备配置

两种方式任选其一：

### 方式 A：零配置 + 网页引导（推荐）

不准备任何配置文件，直接启动（见第 2 节），然后浏览器打开
`http://服务器:8787/admin/setup`（或反代后的
`https://proxy.example.com/admin/setup`），在网页上一次性设置
**admin_key（必填）**、**api_key（可选）** 和**第一个渠道（可选）**。
提交后配置文件自动写入 `data/config.yaml`，引导页随即永久关闭，
后续一切渠道管理都在 `/admin/` 管理台完成。

> 安全说明：仅当 `admin_key` 未设置时 `/admin/setup` 才对远程开放；
> 一旦初始化完成，整个 `/admin/` 立即回到「回环或 Bearer admin_key」
> 的常规保护之下。若你希望走方式 A，请尽快完成初始化，
> 不要长期把未初始化的实例暴露在公网。

### 方式 B：手写配置文件

在服务器上创建数据目录，写入初始 `config.yaml`：

```bash
mkdir -p data
cp config.example.yaml data/config.yaml
```

最小改动项（其余字段见 config.example.yaml 注释）：

```yaml
server:
  listen: ":8787"                # 容器内监听，保持 8787
  api_key: "sk-客户端要填的密钥"    # 必填，公网暴露不能留空
  admin_key: "管理台访问密钥"       # 必填，网页管理台的 Bearer 密钥
  enabled: true

channels:
  - name: openai
    type: openai
    base_url: "https://api.openai.com"   # 上游 base_url，网页上也能改
    api_keys: ["${OPENAI_KEY}"]           # 推荐环境变量引用
    models: ["gpt-4o", "gpt-*"]
    priority: 10
    enabled: true
```

> 密钥不必明文落盘：`api_keys` 里写 `${VAR_NAME}`，在 compose 的
> `environment` 或 `.env` 文件里提供真实值。

## 2. 启动

```bash
mkdir -p data                  # 零配置方式下只需一个空目录
docker compose up -d --build
docker compose logs -f        # 确认 "proxy listening"；未初始化时会提示 setup 地址
```

> 容器以非 root 用户（uid 10001）运行，若挂载宿主机目录遇权限问题：
> `sudo chown -R 10001:10001 data`。

`data/` 目录会持久化 `config.yaml`、`stats.json`（流量统计）、
`cooldowns.json`（密钥冷却状态），重建容器不丢数据。

不装 Docker 时也可以直接运行二进制：

```bash
GOOS=linux go build -o relayhub ./cmd/headless
./relayhub /path/to/config.yaml        # 配合 systemd 常驻
```

## 3. HTTPS 反代

公网部署务必加 HTTPS，否则 api_key / admin_key 会以明文传输。

### 方案 A：Caddy（推荐，自动证书）

```
# Caddyfile
proxy.example.com {
    reverse_proxy 127.0.0.1:8787
}
```

此时可把 compose 端口绑定改为 `127.0.0.1:8787:8787`，
只让 Caddy 对外。

### 方案 B：Nginx + certbot

```nginx
server {
    listen 443 ssl;
    server_name proxy.example.com;

    ssl_certificate     /etc/letsencrypt/live/proxy.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/proxy.example.com/privkey.pem;

    # LLM 请求是长连接 + 流式，必须关缓冲、拉长超时
    proxy_buffering off;
    proxy_read_timeout 600s;

    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
    }
}
```

## 4. 客户端与管理台使用

| 角色 | Base URL | 密钥 |
|---|---|---|
| LLM 客户端 | `https://proxy.example.com/v1` | `server.api_key` |
| 管理台 | `https://proxy.example.com/admin/` | `server.admin_key`（Bearer） |

管理台里可在线增删渠道、改 base_url / api_keys / 模型映射、
切换密钥策略、查看流量统计与请求追踪，全部即时生效。

## 5. 安全清单

- [ ] `api_key`、`admin_key` 均已设置且互不相同
- [ ] 反代启用 HTTPS；容器端口只绑 `127.0.0.1`
- [ ] 防火墙仅放行 80/443，不放行 8787
- [ ] 上游密钥用 `${ENV_VAR}` 引用，不进镜像、不进 git
- [ ] 定期备份 `data/` 目录

## 6. 常见问题

**日志在哪？** 容器内默认写到 `~/.config/RelayHub/logs`
（即 `/home/relayhub/.config/...`）。生产上建议直接看
`docker compose logs`，或在 config.yaml 的 `logging.dir` 指到
`/data/logs` 一并持久化。

**改配置要重启吗？** 不用。改 `data/config.yaml` 约 0.5s 内热加载；
管理台上的所有操作也是即时生效。

**时区/上游代理？** 时区用 compose 的 `TZ` 环境变量；出口需走代理时
给容器加 `HTTPS_PROXY=http://...` 即可（Go 标准库自动识别）。
