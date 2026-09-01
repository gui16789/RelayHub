package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// A tiny mock OpenAI-compatible upstream used to smoke-test the proxy:
// requests carrying the "bad" key get 401, everything else returns a fixed completion.
func main() {
	http.HandleFunc("/v1/models", func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"object": "list",
			"data": []any{
				map[string]any{"id": "gpt-4o", "object": "model", "owned_by": "mock"},
				map[string]any{"id": "gpt-4o-mini", "object": "model", "owned_by": "mock"},
				map[string]any{"id": "deepseek-chat", "object": "model", "owned_by": "mock"},
				map[string]any{"id": "deepseek-reasoner", "object": "model", "owned_by": "mock"},
			},
		})
	})

	http.HandleFunc("/v1/chat/completions", func(writer http.ResponseWriter, request *http.Request) {
		auth := request.Header.Get("Authorization")
		if auth == "Bearer sk-bad-key" {
			writer.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(writer, `{"error":{"message":"invalid api key"}}`)
			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"created": 1700000000,
			"model":   "gpt-4o-mini",
			"choices": []any{map[string]any{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "hello from mock"},
				"finish_reason": "stop",
			}},
		})
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:9999", nil))
}
