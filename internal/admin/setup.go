package admin

import (
	"encoding/json"
	"net/http"

	"github.com/local/relayhub/internal/config"
)

// NeedsSetup reports whether first-boot initialization is still open: no
// admin key has been configured, so /admin/setup and /admin/api/setup
// accept remote clients. Once an admin key exists the wizard closes and
// the normal console auth rules apply.
func (s *Server) NeedsSetup() bool {
	return s.store.Snapshot().Server.AdminKey == ""
}

// handleSetup is the first-boot wizard API.
//
//	GET  /admin/api/setup -> {"ok":true,"needs_setup":bool}
//	POST /admin/api/setup {admin_key, api_key?, channel?} -> initializes the
//	     server in one shot. Refused (403) once an admin key exists.
func (s *Server) handleSetup(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(writer, map[string]any{"ok": true, "needs_setup": s.NeedsSetup()})
	case http.MethodPost:
		if !s.NeedsSetup() {
			writeError(writer, http.StatusForbidden, "setup already completed")
			return
		}
		var payload struct {
			AdminKey string `json:"admin_key"`
			APIKey   string `json:"api_key"`
			Channel  *struct {
				Name    string   `json:"name"`
				Type    string   `json:"type"`
				BaseURL string   `json:"base_url"`
				APIKeys []string `json:"api_keys"`
				Models  []string `json:"models"`
			} `json:"channel"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		var channel *config.Channel
		if payload.Channel != nil && payload.Channel.Name != "" {
			channel = &config.Channel{
				Name:    payload.Channel.Name,
				Type:    payload.Channel.Type,
				BaseURL: payload.Channel.BaseURL,
				APIKeys: payload.Channel.APIKeys,
				Models:  payload.Channel.Models,
			}
		}
		if err := s.store.ApplySetup(payload.AdminKey, payload.APIKey, channel); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		s.collector.PushEvent("info", "", "首次初始化完成（setup wizard）")
		writeJSON(writer, map[string]any{"ok": true, "needs_setup": false})
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// serveSetup renders the first-boot wizard. When setup is already done it
// sends the browser to the normal console instead.
func (s *Server) serveSetup(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/setup" {
		http.NotFound(writer, request)
		return
	}
	if !s.NeedsSetup() {
		http.Redirect(writer, request, "/admin/", http.StatusFound)
		return
	}
	writer.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Write([]byte(setupHTML))
}

const setupHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>RelayHub 首次初始化</title>
<style>
  :root { color-scheme: light; }
  * { box-sizing: border-box; }
  body { margin: 0; font-family: "Segoe UI", "Microsoft YaHei", sans-serif;
         background: #f1f5f9; color: #0f172a; display: flex; justify-content: center; padding: 40px 16px; }
  .card { background: #fff; border-radius: 12px; box-shadow: 0 4px 24px rgba(15,23,42,.08);
          max-width: 560px; width: 100%; padding: 32px; }
  h1 { font-size: 22px; margin: 0 0 4px; }
  .sub { color: #64748b; font-size: 14px; margin-bottom: 24px; }
  fieldset { border: 1px solid #e2e8f0; border-radius: 8px; margin: 0 0 16px; padding: 16px; }
  legend { font-size: 13px; font-weight: 600; color: #475569; padding: 0 6px; }
  label { display: block; font-size: 13px; margin: 10px 0 4px; }
  input, select { width: 100%; padding: 8px 10px; border: 1px solid #cbd5e1; border-radius: 6px;
                  font-size: 14px; font-family: inherit; }
  input:focus, select:focus { outline: 2px solid #6366f1; border-color: transparent; }
  .hint { font-size: 12px; color: #94a3b8; margin-top: 4px; }
  button { width: 100%; padding: 10px; border: 0; border-radius: 8px; background: #4f46e5;
           color: #fff; font-size: 15px; font-weight: 600; cursor: pointer; }
  button:hover { background: #4338ca; }
  button:disabled { background: #a5b4fc; cursor: wait; }
  #msg { margin-top: 14px; font-size: 14px; white-space: pre-wrap; }
  .err { color: #dc2626; }
  .ok { color: #16a34a; }
  #done { display: none; }
  #done code { background: #f1f5f9; padding: 2px 6px; border-radius: 4px; }
</style>
</head>
<body>
<div class="card">
  <h1>RelayHub 首次初始化</h1>
  <div class="sub">服务器上还没有管理密钥。请完成以下设置，初始化后本页自动关闭。</div>

  <form id="form">
    <fieldset>
      <legend>访问密钥</legend>
      <label>管理台密钥 admin_key *</label>
      <input name="admin_key" required minlength="8" autocomplete="new-password">
      <div class="hint">用于打开 /admin/ 管理台（Bearer 鉴权），至少 8 位。</div>
      <label>客户端密钥 api_key（可选）</label>
      <input name="api_key" autocomplete="off">
      <div class="hint">LLM 客户端调用 /v1/* 时需携带的 Bearer 密钥；留空表示不鉴权（公网部署强烈建议设置）。</div>
    </fieldset>

    <fieldset>
      <legend>第一个渠道（可选，稍后可在管理台添加）</legend>
      <label>名称</label>
      <input name="ch_name" placeholder="openai">
      <label>类型</label>
      <select name="ch_type">
        <option value="openai">openai（OpenAI 兼容透传）</option>
        <option value="anthropic">anthropic（自动格式转换）</option>
        <option value="gemini">gemini（自动格式转换）</option>
      </select>
      <label>上游 base_url</label>
      <input name="ch_base_url" placeholder="https://api.openai.com">
      <label>上游 API Key</label>
      <input name="ch_api_key" placeholder="sk-...">
      <label>模型（逗号分隔，支持通配符）</label>
      <input name="ch_models" placeholder="gpt-4o, gpt-4o-mini">
    </fieldset>

    <button type="submit" id="submit">完成初始化</button>
  </form>

  <div id="msg"></div>
  <div id="done">
    <p class="ok">初始化成功！</p>
    <p>管理台：<a href="/admin/" id="adminLink">/admin/</a>（请求头携带 <code>Authorization: Bearer &lt;admin_key&gt;</code>）<br>
    客户端：Base URL 填 <code id="baseURL"></code>，API Key 填你设置的 api_key。</p>
  </div>
</div>

<script>
var form = document.getElementById("form");
var msg = document.getElementById("msg");
form.addEventListener("submit", function (e) {
  e.preventDefault();
  msg.textContent = ""; msg.className = "";
  var fd = new FormData(form);
  var body = {
    admin_key: fd.get("admin_key").trim(),
    api_key: fd.get("api_key").trim()
  };
  var chName = fd.get("ch_name").trim();
  if (chName) {
    body.channel = {
      name: chName,
      type: fd.get("ch_type"),
      base_url: fd.get("ch_base_url").trim(),
      api_keys: fd.get("ch_api_key").trim() ? [fd.get("ch_api_key").trim()] : [],
      models: fd.get("ch_models").split(",").map(function (s) { return s.trim(); }).filter(Boolean)
    };
  }
  var btn = document.getElementById("submit");
  btn.disabled = true;
  fetch("/admin/api/setup", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body)
  }).then(function (r) { return r.json(); }).then(function (data) {
    btn.disabled = false;
    if (!data.ok) {
      msg.textContent = "失败：" + (data.error || "未知错误");
      msg.className = "err";
      return;
    }
    form.style.display = "none";
    document.getElementById("baseURL").textContent = location.origin + "/v1";
    document.getElementById("done").style.display = "block";
  }).catch(function (err) {
    btn.disabled = false;
    msg.textContent = "请求失败：" + err;
    msg.className = "err";
  });
});
</script>
</body>
</html>`
