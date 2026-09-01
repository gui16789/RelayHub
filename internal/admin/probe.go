package admin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/local/relayhub/internal/config"
)

// probeClient is dedicated to model-list probes: short timeout, no retries.
var probeClient = &http.Client{Timeout: 20 * time.Second}

const maxProbedModels = 500

// probeRequest is what the channel form sends for "one-click fetch models".
type probeRequest struct {
	Channel string   `json:"channel"` // channel being edited; fallback key source
	Type    string   `json:"type"`
	BaseURL string   `json:"base_url"`
	APIKeys []string `json:"api_keys"`
}

// handleProbe queries the upstream's model-list API with the form's
// base_url and keys and returns the model ids so the form can pre-fill
// the models field. It persists nothing. When the form has no keys (e.g.
// the key field is being kept as-is on an existing channel) it falls back
// to the keys already stored for that channel.
func (s *Server) handleProbe(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload probeRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeError(writer, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	keys := payload.APIKeys
	if len(keys) == 0 && payload.Channel != "" {
		channel, err := s.store.GetChannel(payload.Channel)
		if err != nil {
			writeError(writer, http.StatusBadRequest, "该渠道还没有配置 API Key，请先填写")
			return
		}
		keys = channel.APIKeys
	}
	if len(keys) == 0 {
		writeError(writer, http.StatusBadRequest, "至少需要一个 API Key（在上方填写，或先保存渠道）")
		return
	}
	models, err := ProbeModels(payload.Type, payload.BaseURL, keys)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	s.collector.PushEvent("info", payload.Type,
		"fetched "+strconv.Itoa(len(models))+" models from "+strings.TrimSpace(payload.BaseURL))
	writeJSON(writer, map[string]any{"ok": true, "models": models})
}

// ProbeModels dispatches to the per-protocol model-list probe. An empty
// type is treated as OpenAI-compatible, mirroring config normalization.
func ProbeModels(channelType, baseURL string, apiKeys []string) ([]string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, fmt.Errorf("base_url 不能为空")
	}
	// The relays append their own versioned path, so drop a trailing /v1.
	if strings.HasSuffix(baseURL, "/v1") {
		baseURL = strings.TrimSuffix(baseURL, "/v1")
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	switch channelType {
	case "", config.TypeOpenAI:
		return ProbeOpenAIModels(baseURL, apiKeys)
	case config.TypeAnthropic:
		return ProbeAnthropicModels(baseURL, apiKeys)
	case config.TypeGemini:
		return ProbeGeminiModels(baseURL, apiKeys)
	default:
		return nil, fmt.Errorf("unsupported type %q", channelType)
	}
}

// ListModels is the per-protocol model-list probe, exported so the relay's
// background health check can reuse the exact same upstream calls.
func ListModels(channelType, baseURL string, apiKeys []string) ([]string, error) {
	return ProbeModels(channelType, baseURL, apiKeys)
}

// probeOpenAI queries {base}/v1/models first, falling back to {base}/models
// for relay-gateway style base URLs.
//
// The fallback is deliberately conservative: an auth rejection (401/403) or
// rate limit (429) on the primary path means the KEY is not allowed, so
// retrying a second path is pointless and (worse) tends to return the
// gateway's HTML landing page, which would mask the real error. Only a
// "wrong path" answer (404/405/501) triggers the fallback.
func ProbeOpenAIModels(baseURL string, apiKeys []string) ([]string, error) {
	auth := func(header http.Header, key string) { header.Set("Authorization", "Bearer "+key) }

	body, status, err := fetchWithKeys(baseURL+"/v1/models", apiKeys, auth)
	if err == nil {
		return parseModelList(body)
	}
	switch status {
	case http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		// Primary path does not exist; try the gateway-style path.
		fallbackBody, fallbackStatus, fallbackErr := fetchWithKeys(baseURL+"/models", apiKeys, auth)
		if fallbackErr == nil {
			models, parseErr := parseModelList(fallbackBody)
			if parseErr == nil {
				return models, nil
			}
			return nil, fmt.Errorf("%s（回退路径 %d 也未返回模型列表）", errText(err), fallbackStatus)
		}
		return nil, err
	default:
		// Auth / rate-limit / network / server error: surface as-is.
		return nil, err
	}
}

func ProbeAnthropicModels(baseURL string, apiKeys []string) ([]string, error) {
	body, _, err := fetchWithKeys(baseURL+"/v1/models", apiKeys, func(header http.Header, key string) {
		header.Set("x-api-key", key)
		header.Set("anthropic-version", "2023-06-01")
	})
	if err != nil {
		return nil, err
	}
	return parseModelList(body)
}

// probeGemini reads {base}/v1beta/models; upstream names carry a
// "models/" prefix that the relay does not use, so it is stripped.
func ProbeGeminiModels(baseURL string, apiKeys []string) ([]string, error) {
	body, _, err := fetchWithKeys(baseURL+"/v1beta/models", apiKeys, func(header http.Header, key string) {
		header.Set("x-goog-api-key", key)
	})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("upstream response is not a model list: %s", snippet(body))
	}
	names := make([]string, 0, len(payload.Models))
	for _, model := range payload.Models {
		names = append(names, strings.TrimPrefix(model.Name, "models/"))
	}
	return dedupeSorted(names), nil
}

// fetchWithKeys tries the URL once per key until one succeeds, so a single
// dead key does not block the probe when the channel lists several. It
// returns the status code of the last attempt so callers can tell a "wrong
// path" (404) apart from a "key not allowed" (401/403) answer.
func fetchWithKeys(url string, apiKeys []string, setKey func(header http.Header, key string)) (body []byte, statusCode int, err error) {
	var lastErr error
	for _, key := range apiKeys {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			return nil, 0, err
		}
		setKey(req.Header, key)
		req.Header.Set("Accept", "application/json")
		// Some gateways serve an HTML anti-bot page to the default Go UA.
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; RelayHub/1.0)")
		resp, err := probeClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("upstream unreachable: %s", errText(err))
			statusCode = 0
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
		resp.Body.Close()
		statusCode = resp.StatusCode
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("upstream %d: %s", resp.StatusCode, upstreamError(raw))
			continue
		}
		return raw, resp.StatusCode, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no api keys provided")
	}
	return nil, statusCode, lastErr
}

// parseModelList extracts data[].id from an OpenAI/Anthropic style list.
func parseModelList(body []byte) ([]string, error) {
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		if isHTML(body) {
			return nil, fmt.Errorf("上游该路径返回的是网页而不是模型列表，请确认 Base URL 是 API 地址（不要带 /v1 等路径前缀）")
		}
		return nil, fmt.Errorf("upstream response is not a model list: %s", snippet(body))
	}
	ids := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		ids = append(ids, item.ID)
	}
	return dedupeSorted(ids), nil
}

// dedupeSorted trims, dedupes and sorts model names; empty names are dropped.
func dedupeSorted(models []string) []string {
	seen := make(map[string]bool, len(models))
	result := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" || seen[model] {
			continue
		}
		seen[model] = true
		result = append(result, model)
	}
	sort.Strings(result)
	if len(result) > maxProbedModels {
		result = result[:maxProbedModels]
	}
	return result
}

// upstreamError extracts a human-readable message from a non-200 upstream
// body. It handles the common JSON shapes — OpenAI {"error":{"message":...}},
// New API {"error":{"message":...}} / {"message":...}, Anthropic
// {"error":{"type","message"}} — and falls back to a one-line snippet for
// anything else (e.g. an HTML error page).
func upstreamError(body []byte) string {
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Error.Message != "" {
			return strings.TrimSpace(payload.Error.Message)
		}
		if payload.Message != "" {
			return strings.TrimSpace(payload.Message)
		}
	}
	return snippet(body)
}

// snippet turns a raw upstream body into a short one-line error message.
func snippet(body []byte) string {
	text := strings.TrimSpace(string(body))
	text = strings.ReplaceAll(text, "\n", " ")
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	if text == "" {
		return "(empty body)"
	}
	return text
}

// isHTML reports whether the body looks like an HTML document (gateways
// often serve their dashboard at non-API paths).
func isHTML(body []byte) bool {
	trimmed := strings.TrimSpace(strings.ToLower(string(body[:min(512, len(body))])))
	return strings.HasPrefix(trimmed, "<!doctype html") || strings.HasPrefix(trimmed, "<html")
}

func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return strings.TrimSpace(err.Error())
}
