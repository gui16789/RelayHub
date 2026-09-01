package relay

// The anthropic/gemini converters rebuild the upstream payload from a
// text-only model, so features they cannot express must not be answered as if
// the client never asked. These tests pin the two halves of that contract:
// such a request is steered to a passthrough (openai) channel when one serves
// the model, and rejected with a naming error when none does.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func featureBody(t *testing.T, extra map[string]any) string {
	t.Helper()
	payload := map[string]any{
		"model":    "m",
		"messages": []map[string]any{{"role": "user", "content": "hi"}},
	}
	for key, value := range extra {
		payload[key] = value
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const anthropicOnlyConfig = `server:
  listen: ":0"
channels:
  - name: claude
    type: anthropic
    base_url: http://127.0.0.1:1
    api_keys: [sk-ant]
    models: [m]
    priority: 10
`

func TestUnconvertibleFeatureRejectedWhenNoPassthroughChannel(t *testing.T) {
	toolsPayload := []map[string]any{{
		"type":     "function",
		"function": map[string]any{"name": "get_weather"},
	}}

	cases := []struct {
		name      string
		extra     map[string]any
		wantNamed string
	}{
		{"tools", map[string]any{"tools": toolsPayload}, "tools"},
		{"tool_choice", map[string]any{"tool_choice": "auto"}, "tool_choice"},
		{
			"response_format",
			map[string]any{"response_format": map[string]any{"type": "json_object"}},
			"response_format=json_object",
		},
		{"n", map[string]any{"n": 2}, "n>1"},
		{
			"vision",
			map[string]any{"messages": []map[string]any{{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": "what is this?"},
					{"type": "image_url", "image_url": map[string]any{"url": "https://example.test/a.png"}},
				},
			}}},
			"image_url",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			handler := setupHardening(t, anthropicOnlyConfig)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(featureBody(t, testCase.extra)))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", recorder.Code, recorder.Body.String())
			}
			body := recorder.Body.String()
			if !strings.Contains(body, testCase.wantNamed) {
				t.Errorf("error should name %q, got: %s", testCase.wantNamed, body)
			}
			// The message has to be actionable, not just a refusal.
			if !strings.Contains(body, "openai") {
				t.Errorf("error should point at the openai channel type, got: %s", body)
			}
		})
	}
}

// A plain text request must still reach an anthropic channel: the capability
// check must not become a blanket ban on converted channels.
func TestPlainRequestStillRoutesToAnthropic(t *testing.T) {
	handler := setupHardening(t, anthropicOnlyConfig)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(featureBody(t, nil)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	// base_url is a dead port, so the attempt fails and the loop reports 502.
	// The point is that it was attempted at all rather than rejected as 400.
	if recorder.Code == http.StatusBadRequest {
		t.Fatalf("plain request must not be rejected as unconvertible: %s", recorder.Body.String())
	}
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 from the unreachable upstream, got %d: %s",
			recorder.Code, recorder.Body.String())
	}
}

// Anthropic requires max_tokens, Gemini does not and defaults to the model's
// own limit. Applying Anthropic's mandatory-field default to Gemini would cap
// every uncapped request at 4096 tokens.
func TestMaxTokensDefaultAppliesToAnthropicOnly(t *testing.T) {
	var noLimit chatRequest
	if got := noLimit.effectiveMaxTokens(); got != anthropicDefaultMaxTokens {
		t.Errorf("anthropic needs a non-zero default, got %d", got)
	}
	if got := buildGeminiRequest(noLimit).GenerationConfig.MaxOutputTokens; got != 0 {
		t.Errorf("gemini must not be capped when the client set no limit, got %d", got)
	}

	limit := 100
	explicit := chatRequest{MaxTokens: &limit}
	if got := explicit.effectiveMaxTokens(); got != limit {
		t.Errorf("anthropic should honor the client limit, got %d", got)
	}
	if got := buildGeminiRequest(explicit).GenerationConfig.MaxOutputTokens; got != limit {
		t.Errorf("gemini should honor the client limit, got %d", got)
	}
}

// With a passthrough channel available the request must be routed there
// instead of rejected, even though an anthropic channel also serves the model.
func TestUnconvertibleFeatureRoutesToOpenAIChannel(t *testing.T) {
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		gotBody, _ = io.ReadAll(request.Body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"1","object":"chat.completion","model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	// The anthropic channel has the higher priority, so routing to the openai
	// one proves the capability filter overrode priority order.
	handler := setupHardening(t, `server:
  listen: ":0"
channels:
  - name: claude
    type: anthropic
    base_url: http://127.0.0.1:1
    api_keys: [sk-ant]
    models: [m]
    priority: 100
  - name: passthrough
    type: openai
    base_url: `+upstream.URL+`
    api_keys: [sk-openai]
    models: [m]
    priority: 1
`)

	body := featureBody(t, map[string]any{"tools": []map[string]any{{
		"type":     "function",
		"function": map[string]any{"name": "get_weather"},
	}}})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200 from the passthrough channel, got %d: %s",
			recorder.Code, recorder.Body.String())
	}
	// tools must survive verbatim; the whole point of picking this channel.
	if !strings.Contains(string(gotBody), "get_weather") {
		t.Errorf("tools should reach the upstream unchanged, got: %s", gotBody)
	}
}
