package relay

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxBufferedResponse caps how much of a non-streaming upstream response is
// held in memory for usage extraction; larger bodies are streamed through
// with the remaining bytes after the cap.
const maxBufferedResponse = 32 << 20

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

func writeJSONResponse(writer http.ResponseWriter, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(writer).Encode(payload)
}

func setStreamingHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.WriteHeader(http.StatusOK)
}

// maybeInjectIncludeUsage ensures stream_options.include_usage=true on an
// OpenAI-style streaming request so the upstream reports token usage in the
// final chunk. Non-streaming or unparseable bodies pass through unchanged.
// It returns the (possibly rewritten) body, whether the body was modified,
// and whether the request is a stream.
func maybeInjectIncludeUsage(body []byte) (sent []byte, injected bool, stream bool) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false, false
	}
	if stream, _ = payload["stream"].(bool); !stream {
		return body, false, false
	}
	opts, _ := payload["stream_options"].(map[string]any)
	if opts == nil {
		opts = map[string]any{}
	}
	if include, ok := opts["include_usage"].(bool); !ok || !include {
		opts["include_usage"] = true
		payload["stream_options"] = opts
		rewritten, err := json.Marshal(payload)
		if err != nil {
			return body, false, stream
		}
		return rewritten, true, stream
	}
	return body, false, stream
}

// rewriteModel replaces the top-level "model" field of an OpenAI-style JSON
// body with the channel's upstream model name (model_map). Unparseable
// bodies pass through unchanged.
func rewriteModel(body []byte, upstreamModel string) []byte {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	payload["model"] = upstreamModel
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return rewritten
}

// usageFromOpenAIJSON extracts usage from a non-streaming OpenAI-style body.
func usageFromOpenAIJSON(raw []byte) (prompt, completion int) {
	var payload struct {
		Usage *chatUsage `json:"usage"`
	}
	if json.Unmarshal(raw, &payload) == nil && payload.Usage != nil {
		return payload.Usage.PromptTokens, payload.Usage.CompletionTokens
	}
	return 0, 0
}

// rewriteModelField replaces the top-level "model" field of a non-streaming
// OpenAI-style response body (the inverse of rewriteModel, used to restore
// the client-facing name after model_map remapped it upstream).
func rewriteModelField(raw []byte, modelOverride string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	current, _ := obj["model"].(string)
	if current == modelOverride {
		return raw
	}
	obj["model"] = modelOverride
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return rewritten
}

// rewriteSSEModel replaces the "model" field of one SSE data line with
// modelOverride, preserving the original line terminator. Lines that are
// not parseable JSON chunks (or already carry the right model) pass through
// untouched.
func rewriteSSEModel(line, modelOverride string) string {
	payload, ok := sseData(line)
	if !ok {
		return line
	}
	var obj map[string]any
	if json.Unmarshal(payload, &obj) != nil {
		return line
	}
	current, _ := obj["model"].(string)
	if current == modelOverride {
		return line
	}
	obj["model"] = modelOverride
	rewritten, err := json.Marshal(obj)
	if err != nil {
		return line
	}
	terminator := ""
	switch {
	case strings.HasSuffix(line, "\r\n"):
		terminator = "\r\n"
	case strings.HasSuffix(line, "\n"):
		terminator = "\n"
	}
	return "data: " + string(rewritten) + terminator
}

// scanOpenAIStream copies an OpenAI-style SSE stream to the writer verbatim
// (flushing line by line) and returns the usage from the final chunk when
// the upstream included one. When modelOverride is non-empty the per-chunk
// model field is rewritten to the client-facing name.
//
// The returned error distinguishes a clean end (nil, EOF or [DONE]) from an
// abnormal one: a write failure means the CLIENT went away mid-stream; a
// read failure means the UPSTREAM truncated the stream. Both are reported
// because the attempt still counts as served and would otherwise look clean.
func scanOpenAIStream(writer http.ResponseWriter, body io.Reader, modelOverride string) (prompt, completion int, err error) {
	flusher, canFlush := writer.(http.Flusher)
	reader := bufio.NewReaderSize(body, 64*1024)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			if modelOverride != "" {
				line = rewriteSSEModel(line, modelOverride)
			}
			if _, writeErr := writer.Write([]byte(line)); writeErr != nil {
				return prompt, completion, fmt.Errorf("client disconnected mid-stream: %w", writeErr)
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") && !strings.Contains(trimmed, "[DONE]") {
				if payload, ok := sseData(line); ok {
					var chunk struct {
						Usage *chatUsage `json:"usage"`
					}
					if json.Unmarshal(payload, &chunk) == nil && chunk.Usage != nil {
						prompt = chunk.Usage.PromptTokens
						completion = chunk.Usage.CompletionTokens
					}
				}
			}
			if canFlush {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return prompt, completion, nil
			}
			return prompt, completion, fmt.Errorf("upstream truncated the stream: %w", readErr)
		}
	}
}
