package relay

// Token accounting tests: each protocol (openai / anthropic / gemini) must
// capture upstream usage for both streaming and non-streaming responses, and
// the OpenAI-style relay must inject stream_options.include_usage on streams.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

func setupRelayForTokens(t *testing.T, upstreamURL string, channelType, model string) (*Handler, *stats.Collector) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `server:
  listen: ":0"
channels:
  - name: ch
    type: ` + channelType + `
    base_url: ` + upstreamURL + `
    api_keys:
      - sk-test
    models:
      - ` + model + `
    priority: 1
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := store.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	collector := stats.NewCollector()
	handler := NewHandler(store, collector)
	return handler, collector
}

func chatBody(model string, stream bool) []byte {
	payload, _ := json.Marshal(map[string]any{
		"model":    model,
		"stream":   stream,
		"messages": []map[string]string{{"role": "user", "content": "hi"}},
	})
	return payload
}

func postModel(t *testing.T, handler http.Handler, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func assertTokens(t *testing.T, collector *stats.Collector, prompt, completion int) {
	t.Helper()
	summary := collector.Summary(true)
	if summary.TotalPromptTokens != uint64(prompt) {
		t.Errorf("prompt tokens: got %d, want %d", summary.TotalPromptTokens, prompt)
	}
	if summary.TotalCompletionTokens != uint64(completion) {
		t.Errorf("completion tokens: got %d, want %d", summary.TotalCompletionTokens, completion)
	}
}

func TestTokenAccounting(t *testing.T) {
	t.Run("openai non-streaming", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":22,"total_tokens":33}}`))
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "openai", "m1")
		rec := postModel(t, handler, chatBody("m1", false))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertTokens(t, collector, 11, 22)
	})

	t.Run("openai streaming captures final chunk usage", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// The relay must have injected stream_options.include_usage.
			body, _ := io.ReadAll(r.Body)
			var sent map[string]any
			if err := json.Unmarshal(body, &sent); err != nil {
				t.Fatalf("bad body: %v", err)
			}
			opts, _ := sent["stream_options"].(map[string]any)
			if opts == nil || opts["include_usage"] != true {
				t.Errorf("stream_options.include_usage not injected, sent body: %s", body)
			}

			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"he\"}}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			flusher.Flush()
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "openai", "m1")
		rec := postModel(t, handler, chatBody("m1", true))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "\"he\"") {
			t.Fatalf("stream content lost: %s", rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "[DONE]") {
			t.Fatalf("[DONE] missing: %s", rec.Body.String())
		}
		assertTokens(t, collector, 5, 7)
	})

	t.Run("anthropic non-streaming", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":13,"output_tokens":17}}`))
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "anthropic", "m1")
		rec := postModel(t, handler, chatBody("m1", false))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertTokens(t, collector, 13, 17)
	})

	t.Run("anthropic streaming", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			events := []string{
				`{"type":"message_start","message":{"usage":{"input_tokens":21,"output_tokens":1}}}`,
				`{"type":"content_block_delta","delta":{"type":"text_delta","text":"hello"}}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn","usage":{"output_tokens":23}}}`,
			}
			for _, event := range events {
				_, _ = w.Write([]byte("event: " + strings.Fields(event)[0] + "\n"))
				_, _ = w.Write([]byte("data: " + event + "\n\n"))
				flusher.Flush()
			}
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "anthropic", "m1")
		rec := postModel(t, handler, chatBody("m1", true))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "hello") {
			t.Fatalf("stream content lost: %s", rec.Body.String())
		}
		assertTokens(t, collector, 21, 23)
	})

	t.Run("gemini non-streaming", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":"gemini says hi"}]}}],"usageMetadata":{"promptTokenCount":31,"candidatesTokenCount":33}}`))
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "gemini", "m1")
		rec := postModel(t, handler, chatBody("m1", false))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertTokens(t, collector, 31, 33)
	})

	t.Run("gemini streaming", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"gem\"}]}}]}\n\n"))
			flusher.Flush()
			_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ini\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":41,\"candidatesTokenCount\":43}}\n\n"))
			flusher.Flush()
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "gemini", "m1")
		rec := postModel(t, handler, chatBody("m1", true))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "gem") || !strings.Contains(rec.Body.String(), "ini") {
			t.Fatalf("stream content lost: %s", rec.Body.String())
		}
		assertTokens(t, collector, 41, 43)
	})

	t.Run("per-channel tokens are attributed", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
		}))
		defer upstream.Close()

		handler, collector := setupRelayForTokens(t, upstream.URL, "openai", "m1")
		postModel(t, handler, chatBody("m1", false))
		postModel(t, handler, chatBody("m1", false))

		summary := collector.Summary(true)
		if len(summary.Channels) != 1 || summary.Channels[0].Name != "ch" {
			t.Fatalf("channel stats missing: %+v", summary.Channels)
		}
		if summary.Channels[0].PromptTokens != 2 || summary.Channels[0].CompletionTokens != 4 {
			t.Fatalf("per-channel tokens wrong: %+v", summary.Channels[0])
		}
	})
}
