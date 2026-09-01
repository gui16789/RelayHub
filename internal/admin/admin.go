package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/local/relayhub/internal/config"
	"github.com/local/relayhub/internal/relay"
	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
	"github.com/local/relayhub/internal/version"
)

// Server exposes the admin REST API and serves the embedded web console.
type Server struct {
	store     *store.Store
	handler   *relay.Handler
	collector *stats.Collector
}

func NewServer(cfgStore *store.Store, handler *relay.Handler, collector *stats.Collector) *Server {
	return &Server{store: cfgStore, handler: handler, collector: collector}
}

// Mux wires all admin routes under /admin/.
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/", s.serveConsole)
	mux.HandleFunc("/admin/api/status", s.handleStatus)
	mux.HandleFunc("/admin/api/server", s.handleServer)
	mux.HandleFunc("/admin/api/key-strategy", s.handleKeyStrategy)
	mux.HandleFunc("/admin/api/proxy/enable", s.handleProxyEnable)
	mux.HandleFunc("/admin/api/channels", s.handleChannels)
	mux.HandleFunc("/admin/api/channels/", s.handleChannelByName)
	mux.HandleFunc("/admin/api/probe-models", s.handleProbe)
	mux.HandleFunc("/admin/api/events", s.handleEvents)
	mux.HandleFunc("/admin/api/traces", s.handleTraces)
	mux.HandleFunc("/admin/api/version", s.handleVersion)
	return mux
}

// channelView is the admin-facing channel shape: keys are masked for safety.
type channelView struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	BaseURL          string            `json:"base_url"`
	APIKeys          []string          `json:"api_keys"`
	Models           []string          `json:"models"`
	ModelMap         map[string]string `json:"model_map"`
	Headers          map[string]string `json:"headers"`
	Priority         int               `json:"priority"`
	Enabled          bool              `json:"enabled"`
	Requests         uint64            `json:"requests"`
	Served           uint64            `json:"served"`
	Failed           uint64            `json:"failed"`
	PromptTokens     uint64            `json:"prompt_tokens"`
	CompletionTokens uint64            `json:"completion_tokens"`
	P50MS            int64             `json:"p50_ms"`
	P95MS            int64             `json:"p95_ms"`
	// Health mirrors the background probe: "" until the first probe lands,
	// then "up"/"down". A down channel is skipped by the router.
	Health string `json:"health"`
}

type statusResponse struct {
	stats.Summary
	ConfigPath string               `json:"config_path"`
	Listen     string               `json:"listen"`
	APIKey     string               `json:"api_key"`
	// KeyStrategy is the global key rotation strategy applied to all channels
	// with multiple keys ("round_robin" or "preferred_first").
	KeyStrategy string               `json:"key_strategy"`
	Channels   []channelView        `json:"channels"`
	Cooldowns  []relay.CooldownInfo `json:"cooldowns"`
}

func (s *Server) handleStatus(writer http.ResponseWriter, request *http.Request) {
	snapshot := s.store.Snapshot()
	summary := s.collector.Summary(snapshot.Server.IsEnabled())
	statByName := make(map[string]stats.ChannelStat, len(summary.Channels))
	for _, channelStat := range summary.Channels {
		statByName[channelStat.Name] = channelStat
	}

	channels := make([]channelView, 0, len(snapshot.Channels))
	for _, channel := range snapshot.Channels {
		view := channelView{
			Name:        channel.Name,
			Type:        channel.Type,
			BaseURL:     channel.BaseURL,
			APIKeys:     maskKeys(channel.APIKeys),
			Models:      channel.Models,
			ModelMap:    channel.ModelMap,
			Headers:     channel.Headers,
			Priority:    channel.Priority,
			Enabled:     channel.IsEnabled(),
		}
		if view.ModelMap == nil {
			view.ModelMap = map[string]string{}
		}
		if view.Headers == nil {
			view.Headers = map[string]string{}
		}
		if channelStat, ok := statByName[channel.Name]; ok {
			view.Requests = channelStat.Requests
			view.Served = channelStat.Served
			view.Failed = channelStat.Failed
			view.PromptTokens = channelStat.PromptTokens
			view.CompletionTokens = channelStat.CompletionTokens
			view.P50MS = channelStat.P50MS
			view.P95MS = channelStat.P95MS
		}
		if status, _, _, ok := s.handler.State().HealthInfo(channel.Name); ok {
			view.Health = status
		}
		channels = append(channels, view)
	}

	writeJSON(writer, statusResponse{
		Summary:     summary,
		ConfigPath:  s.store.Path(),
		Listen:      snapshot.Server.Listen,
		APIKey:      snapshot.Server.APIKey,
		KeyStrategy: snapshot.Server.KeyStrategy,
		Channels:    channels,
		Cooldowns:   s.handler.State().Cooldowns(),
	})
}

// handleServer manages the proxy's own access key (server.api_key) — the
// Bearer token third-party clients must present. Empty means no auth.
func (s *Server) handleServer(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		snapshot := s.store.Snapshot()
		writeJSON(writer, map[string]any{
			"ok": true, "listen": snapshot.Server.Listen, "api_key": snapshot.Server.APIKey,
		})
	case http.MethodPut:
		var payload struct {
			APIKey *string `json:"api_key"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.APIKey == nil {
			writeError(writer, http.StatusBadRequest, "invalid body: api_key is required")
			return
		}
		if err := s.store.SetServerAPIKey(*payload.APIKey); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		state := "关闭"
		if *payload.APIKey != "" {
			state = "开启"
		}
		s.collector.PushEvent("info", "", "接入密钥鉴权"+state)
		writeJSON(writer, map[string]any{"ok": true})
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleKeyStrategy manages the global key rotation strategy applied to every
// channel with multiple keys. GET returns the current value; PUT updates it.
// Accepted values: "round_robin" (default) or "preferred_first".
func (s *Server) handleKeyStrategy(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		snapshot := s.store.Snapshot()
		writeJSON(writer, map[string]any{
			"ok":           true,
			"key_strategy": snapshot.Server.KeyStrategy,
		})
	case http.MethodPut:
		var payload struct {
			KeyStrategy *string `json:"key_strategy"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.KeyStrategy == nil {
			writeError(writer, http.StatusBadRequest, "invalid body: key_strategy is required")
			return
		}
		if err := s.store.SetKeyStrategy(*payload.KeyStrategy); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		state := "轮询切换"
		if *payload.KeyStrategy == config.KeyStrategyPreferredFirst {
			state = "固定首选key"
		}
		s.collector.PushEvent("info", "", "Key切换策略调整为"+state)
		writeJSON(writer, map[string]any{"ok": true})
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// maskKeys shows only the last 4 characters so the console never leaks secrets.
func maskKeys(keys []string) []string {
	masked := make([]string, 0, len(keys))
	for _, key := range keys {
		if len(key) > 8 {
			masked = append(masked, "****"+key[len(key)-4:])
		} else {
			masked = append(masked, "****")
		}
	}
	return masked
}

type enableRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *Server) handleProxyEnable(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body enableRequest
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := s.store.SetProxyEnabled(body.Enabled); err != nil {
		writeError(writer, http.StatusInternalServerError, err.Error())
		return
	}
	state := "disabled"
	if body.Enabled {
		state = "enabled"
	}
	s.collector.PushEvent("info", "", "proxy "+state+" by admin")
	writeJSON(writer, map[string]any{"ok": true, "enabled": body.Enabled})
}

func (s *Server) handleChannels(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		// List through the same masked view as /status: this endpoint must
		// never serialize raw API keys (unmasked keys are only available
		// via the explicit .../keys endpoint for the edit form).
		snapshot := s.store.Snapshot()
		views := make([]channelView, 0, len(snapshot.Channels))
		for _, channel := range snapshot.Channels {
			view := channelView{
				Name:        channel.Name,
				Type:        channel.Type,
				BaseURL:     channel.BaseURL,
				APIKeys:     maskKeys(channel.APIKeys),
				Models:      channel.Models,
				ModelMap:    channel.ModelMap,
				Headers:     channel.Headers,
				Priority:    channel.Priority,
				Enabled:     channel.IsEnabled(),
			}
			if view.ModelMap == nil {
				view.ModelMap = map[string]string{}
			}
			if view.Headers == nil {
				view.Headers = map[string]string{}
			}
			views = append(views, view)
		}
		writeJSON(writer, views)
	case http.MethodPost:
		var channel config.Channel
		if err := json.NewDecoder(request.Body).Decode(&channel); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := s.store.AddChannel(channel); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		s.collector.PushEvent("info", channel.Name, "channel added")
		writeJSON(writer, map[string]any{"ok": true})
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleChannelByName routes /admin/api/channels/{name}, /{name}/toggle,
// /{name}/keys and /{name}/model-map.
func (s *Server) handleChannelByName(writer http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/admin/api/channels/")
	name, isToggle := strings.CutSuffix(path, "/toggle")
	name, isKeys := strings.CutSuffix(name, "/keys")
	name, isModelMap := strings.CutSuffix(name, "/model-map")
	name = strings.TrimSuffix(name, "/")
	if name == "" {
		writeError(writer, http.StatusBadRequest, "channel name required")
		return
	}

	if isModelMap {
		if request.Method != http.MethodPut {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if _, err := s.store.GetChannel(name); err != nil {
			writeError(writer, http.StatusNotFound, err.Error())
			return
		}
		var body struct {
			ModelMap map[string]string `json:"model_map"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if len(body.ModelMap) == 0 {
			body.ModelMap = nil // keep the config file clean: {} means "no mapping"
		}
		if err := s.store.UpdateChannelModelMap(name, body.ModelMap); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		s.collector.PushEvent("info", name, "model_map updated")
		writeJSON(writer, map[string]any{"ok": true})
		return
	}

	if isKeys {
		// Returns UNMASKED keys. This is a local admin endpoint (the console
		// only ever runs against 127.0.0.1 in practice) and exists so the
		// edit form can repopulate the key field and the model probe can
		// reuse stored keys.
		channel, err := s.store.GetChannel(name)
		if err != nil {
			writeError(writer, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(writer, map[string]any{"ok": true, "api_keys": channel.APIKeys})
		return
	}

	if isToggle {
		if request.Method != http.MethodPost {
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body enableRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := s.store.SetChannelEnabled(name, body.Enabled); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		state := "disabled"
		if body.Enabled {
			state = "enabled"
		}
		s.collector.PushEvent("info", name, "channel "+state)
		writeJSON(writer, map[string]any{"ok": true})
		return
	}

	switch request.Method {
	case http.MethodPut:
		var channel config.Channel
		if err := json.NewDecoder(request.Body).Decode(&channel); err != nil {
			writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
			return
		}
		if err := s.store.UpdateChannel(name, channel); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		s.collector.PushEvent("info", channel.Name, "channel updated")
		writeJSON(writer, map[string]any{"ok": true})
	case http.MethodDelete:
		if err := s.store.DeleteChannel(name); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		s.collector.PushEvent("warn", name, "channel deleted")
		writeJSON(writer, map[string]any{"ok": true})
	default:
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleEvents(writer http.ResponseWriter, request *http.Request) {
	events := s.collector.Events(100)
	if events == nil {
		events = []stats.Event{}
	}
	writeJSON(writer, events)
}

// handleTraces returns recent request routing traces (newest first) for the
// console's request-trace feed: which channel/key each hop tried, the
// upstream status, per-hop latency, and where the request finally landed.
func (s *Server) handleTraces(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(writer, s.collector.Traces(100))
}

func writeJSON(writer http.ResponseWriter, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeError(writer http.ResponseWriter, statusCode int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)
	_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error": message})
}

func (s *Server) serveConsole(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/admin/" && request.URL.Path != "/admin" {
		http.NotFound(writer, request)
		return
	}
	// The console is served from the running binary, so an embedded
	// WebView must never serve a cached copy of an older build.
	writer.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	writer.Header().Set("Pragma", "no-cache")
	writer.Header().Set("Expires", "0")
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Write([]byte(consoleHTML))
}

// handleVersion exposes the build-time version to the admin console so
// users can verify they're running the correct release.
func (s *Server) handleVersion(writer http.ResponseWriter, request *http.Request) {
	writeJSON(writer, map[string]any{
		"version":    version.Version,
		"commit":     version.Commit,
		"build_time": version.BuildTime,
		"full":       version.String(),
	})
}
