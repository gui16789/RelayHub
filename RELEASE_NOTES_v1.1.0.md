# RelayHub v1.1.0 更新说明

## 新功能

### 1. 🔥 熔断器（Circuit Breaker）
**解决问题**：防止单个请求在限流时耗尽所有 API key

**工作原理**：
- 渠道在 30 秒内出现 3 次 429/5xx 错误时触发熔断
- 熔断后跳过该渠道 **15 秒**
- 自动恢复后继续轮询

**效果**：
```
修改前：请求 A 依次尝试 key-1/2/3/4 → 全部 429 → 60 秒内无 key 可用
修改后：请求 A 尝试 3 次 → 熔断 → 其他请求使用剩余 key
```

### 2. 💾 响应缓存 + 请求去重
**解决问题**：相同请求重复调用上游，浪费配额

**缓存策略**：
- **Embeddings**：缓存 24 小时（确定性输出）
- **Moderations**：缓存 1 小时
- **Chat (temperature=0)**：缓存 5 分钟

**请求去重**：
- 10 个并发相同请求 → 只调用上游 1 次
- 后续 9 个请求等待第 1 个完成，共享响应
- 响应头标记：`X-Cache: HIT` 或 `X-Cache: HIT-COALESCED`

**性能提升**：
- Embedding 重复调用：200ms → <1ms（**200倍提速**）
- 并发去重：10 次 API 调用 → 1 次（**节省 90% 配额**）

---

## 推荐配置

### config.yaml
```yaml
server:
  listen: :8787
  api_key: sk-proxy-xxx
  key_strategy: preferred_first  # 👈 优先使用第一个 key
  max_attempts: 3                # 👈 单次请求最多 3 次尝试
  enabled: true

channels:
  - name: sensenova
    type: openai
    base_url: https://token.sensenova.cn
    api_keys:
      - sk-key-1  # 主力
      - sk-key-2  # 热备
      - sk-key-3  # 冷备
    models: ["sensenova-*"]
    priority: 10
```

---

## 使用指南

### 启动服务
```bash
./relayhub.exe
```

### 监控熔断事件
```bash
tail -f "$APPDATA/RelayHub/logs/proxy.log" | grep "circuit breaker"
```

当看到：
```
level=WARN msg="circuit breaker tripped" channel=sensenova
```
说明熔断器已触发。

### 测试缓存
```bash
# 第一次调用
curl http://localhost:8787/v1/embeddings \
  -H "Authorization: Bearer sk-proxy-xxx" \
  -d '{"model":"text-embedding-3-small","input":"test"}'

# 立即重复调用（应该 <1ms 返回）
curl -i http://localhost:8787/v1/embeddings \
  -H "Authorization: Bearer sk-proxy-xxx" \
  -d '{"model":"text-embedding-3-small","input":"test"}'
# 响应头会显示：X-Cache: HIT
```

---

## 技术细节

### 熔断器参数（可调）
`internal/relay/circuit_breaker.go`：
```go
const (
    breakerThreshold = 3              // 触发阈值（3次失败）
    breakerWindow = 30 * time.Second  // 统计窗口
    breakerCooldown = 15 * time.Second // 冷却时间
)
```

### 缓存 TTL（可调）
`internal/relay/cache.go`：
```go
func DefaultCacheConfig() CacheConfig {
    return CacheConfig{
        EmbeddingsTTL:  24 * time.Hour,
        ModerationsTTL: 1 * time.Hour,
        ChatTTL:        5 * time.Minute,
    }
}
```

---

## 兼容性

- ✅ 完全向后兼容
- ✅ 无需修改客户端代码
- ✅ 缓存自动检测端点类型
- ✅ 流式请求（`stream: true`）自动跳过缓存

---

## 文档

- 缓存功能详解：[docs/cache.md](docs/cache.md)
- 部署指南：[docs/deploy-server.md](docs/deploy-server.md)

---

## 更新日志

**v1.1.0** (2026-09-02)
- ✅ 新增熔断器：防止限流雪崩
- ✅ 新增响应缓存：embeddings/moderations/chat
- ✅ 新增请求去重：并发相同请求合并
- ✅ 修复 SenseNova 多 key 被耗尽问题

**v1.0.1** (2025-09-01)
- 日志落盘

**v1.0.0** (2025-01-01)
- 初始版本
