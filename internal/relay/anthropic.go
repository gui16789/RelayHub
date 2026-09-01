package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Converts between the OpenAI chat format and the Anthropic /v1/messages API,
// for both streaming and non-streaming responses.

type anthropicBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	TopP        *float64           `json:"top_p,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
}

type anthropicResponse struct {
	Content    []anthropicBlock `json:"content"`
	StopReason string           `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (h *Handler) relayAnthropic(
	writer http.ResponseWriter,
	body []byte,
	attempt attempt,
) attemptResult {
	var parsed chatRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		return attemptResult{outcome: outcomeAborted, err: err}
	}

	upstreamPayload, err := buildAnthropicRequest(parsed, attempt.upstreamModel)
	if err != nil {
		return attemptResult{outcome: outcomeAborted, err: err}
	}

	upstreamRequest, err := http.NewRequest(
		http.MethodPost, attempt.channel.BaseURL+"/v1/messages", bytes.NewReader(mustJSON(upstreamPayload)))
	if err != nil {
		return attemptResult{outcome: outcomeFailed, err: err}
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("x-api-key", attempt.apiKey)
	upstreamRequest.Header.Set("anthropic-version", "2023-06-01")
	applyChannelHeaders(upstreamRequest.Header, attempt.channel.Headers)

	client := h.client
	if parsed.Stream {
		client = h.streamClient
	}
	upstreamResponse, err := client.Do(upstreamRequest)
	if err != nil {
		return handleAttemptError(err, attempt)
	}
	defer upstreamResponse.Body.Close()

	if upstreamResponse.StatusCode != http.StatusOK {
		return h.handleUpstreamError(upstreamResponse, attempt)
	}

	h.state.ResetServerStreak(attempt.channel.Name, attempt.apiKey)
	h.state.ResetQuota(attempt.channel.Name, attempt.apiKey)
	completionID := fmt.Sprintf("chatcmpl-anthropic-%d", time.Now().UnixNano())
	if parsed.Stream {
		return streamAnthropicToOpenAI(writer, upstreamResponse, completionID, parsed.Model)
	}

	var anthropicResult anthropicResponse
	if err := json.NewDecoder(upstreamResponse.Body).Decode(&anthropicResult); err != nil {
		return attemptResult{outcome: outcomeFailed, err: fmt.Errorf("decode anthropic response: %w", err)}
	}
	promptTokens := anthropicResult.Usage.InputTokens
	completionTokens := anthropicResult.Usage.OutputTokens

	responseText := ""
	for _, block := range anthropicResult.Content {
		if block.Type == "text" {
			responseText += block.Text
		}
	}

	writeJSONResponse(writer, chatCompletionResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   parsed.Model,
		Choices: []chatChoiceItem{{
			Index:        0,
			Message:      &chatFullMessage{Role: "assistant", Content: responseText},
			FinishReason: mapAnthropicStopReason(anthropicResult.StopReason),
		}},
		Usage: &chatUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	})
	return attemptResult{
		outcome:          outcomeServed,
		promptTokens:     promptTokens,
		completionTokens: completionTokens,
	}
}

// buildAnthropicRequest maps the parsed OpenAI request into Anthropic format:
// system messages are hoisted and adjacent same-role messages are merged
// (Anthropic requires strict user/assistant alternation).
// upstreamModel is the channel's own name for the model (model_map); the
// client-facing request.Model is only what the response echoes back.
func buildAnthropicRequest(request chatRequest, upstreamModel string) (*anthropicRequest, error) {
	result := &anthropicRequest{
		Model:       upstreamModel,
		MaxTokens:   request.effectiveMaxTokens(),
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stream:      request.Stream,
	}

	for _, message := range request.Messages {
		text := messageText(message.Content)
		if text == "" {
			continue
		}
		switch message.Role {
		case "system", "developer":
			result.System += text + "\n"
		default:
			appendAnthropicMessage(result, message.Role, text)
		}
	}

	if len(result.Messages) == 0 {
		return nil, fmt.Errorf("no usable messages after conversion")
	}
	return result, nil
}

func appendAnthropicMessage(result *anthropicRequest, role, text string) {
	targetRole := role
	if targetRole != "assistant" {
		targetRole = "user"
	}
	lastIndex := len(result.Messages) - 1
	if lastIndex >= 0 && result.Messages[lastIndex].Role == targetRole {
		result.Messages[lastIndex].Content = append(
			result.Messages[lastIndex].Content,
			anthropicBlock{Type: "text", Text: text},
		)
		return
	}
	result.Messages = append(result.Messages, anthropicMessage{
		Role:    targetRole,
		Content: []anthropicBlock{{Type: "text", Text: text}},
	})
}

func mapAnthropicStopReason(stopReason string) any {
	switch stopReason {
	case "max_tokens":
		return "length"
	case "", "end_turn":
		return "stop"
	default:
		return stopReason
	}
}

// streamAnthropicToOpenAI converts Anthropic SSE events into OpenAI chunks live.
func streamAnthropicToOpenAI(
	writer http.ResponseWriter,
	upstreamResponse *http.Response,
	completionID string,
	model string,
) attemptResult {
	flusher, canFlush := writer.(http.Flusher)
	setStreamingHeaders(writer)

	sendChunk := func(delta *chatChoiceDelta, finishReason any) bool {
		chunkBytes := mustJSON(chatChunk{
			ID:      completionID,
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   model,
			Choices: []chatChoiceItem{{Index: 0, Delta: delta, FinishReason: finishReason}},
		})
		if _, err := fmt.Fprintf(writer, "data: %s\n\n", chunkBytes); err != nil {
			return false
		}
		if canFlush {
			flusher.Flush()
		}
		return true
	}

	// OpenAI convention: the first chunk carries the assistant role marker.
	sendChunk(&chatChoiceDelta{Role: "assistant"}, nil)

	scanner := bufio.NewScanner(upstreamResponse.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	// Anthropic reports input_tokens on message_start and output_tokens on
	// message_delta; together they are the stream's usage.
	var promptTokens, completionTokens int

	for scanner.Scan() {
		payload, ok := sseData(scanner.Text())
		if !ok || string(payload) == "[DONE]" {
			continue
		}

		var event struct {
			Type  string `json:"type"`
			Delta json.RawMessage `json:"delta"`
			Message *struct {
				Usage struct {
					InputTokens int `json:"input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(payload, &event) != nil {
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				promptTokens = event.Message.Usage.InputTokens
			}
		case "content_block_delta":
			var delta struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			if json.Unmarshal(event.Delta, &delta) == nil && delta.Type == "text_delta" && delta.Text != "" {
				if !sendChunk(&chatChoiceDelta{Content: delta.Text}, nil) {
					return attemptResult{outcome: outcomeServed, promptTokens: promptTokens, completionTokens: completionTokens}
				}
			}
		case "message_delta":
			var delta struct {
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(event.Delta, &delta) == nil {
				completionTokens = delta.Usage.OutputTokens
			}
			sendChunk(&chatChoiceDelta{}, mapAnthropicStopReason(extractStopReason(event.Delta)))
		}
	}

	fmt.Fprint(writer, "data: [DONE]\n\n")
	if canFlush {
		flusher.Flush()
	}
	result := attemptResult{outcome: outcomeServed, promptTokens: promptTokens, completionTokens: completionTokens}
	if scanErr := scanner.Err(); scanErr != nil {
		result.note = "upstream truncated the stream: " + scanErr.Error()
	}
	return result
}

func extractStopReason(delta json.RawMessage) string {
	var parsed struct {
		StopReason string `json:"stop_reason"`
	}
	if json.Unmarshal(delta, &parsed) == nil {
		return parsed.StopReason
	}
	return ""
}

// sseData extracts the payload from an SSE "data:" line.
func sseData(line string) ([]byte, bool) {
	const prefix = "data:"
	if len(line) < len(prefix) || line[:len(prefix)] != prefix {
		return nil, false
	}
	return bytes.TrimSpace([]byte(line[len(prefix):])), true
}
