package relay

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Converts between the OpenAI chat format and Google Gemini's generateContent
// API, for both streaming and non-streaming responses.

type geminiPart struct {
	Text string `json:"text"`
}

type geminiContent struct {
	Role  string       `json:"role"` // "user" or "model"
	Parts []geminiPart `json:"parts"`
}

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

func (h *Handler) relayGemini(
	writer http.ResponseWriter,
	clientRequest *http.Request,
	body []byte,
	attempt attempt,
) attemptResult {
	var parsed chatRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		return attemptResult{outcome: outcomeAborted, err: err}
	}

	upstreamPayload := buildGeminiRequest(parsed)

	action := ":generateContent"
	if parsed.Stream {
		action = ":streamGenerateContent?alt=sse"
	}
	upstreamURL := fmt.Sprintf("%s/v1beta/models/%s%s", attempt.channel.BaseURL, attempt.upstreamModel, action)

	// Carry the client's context: streamClient has no total timeout, so
	// without cancellation a client that hangs up mid-stream leaves the
	// upstream generating (and billing) until it finishes on its own.
	upstreamRequest, err := http.NewRequestWithContext(clientRequest.Context(),
		http.MethodPost, upstreamURL, bytes.NewReader(mustJSON(upstreamPayload)))
	if err != nil {
		return attemptResult{outcome: outcomeFailed, err: err}
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	upstreamRequest.Header.Set("x-goog-api-key", attempt.apiKey)
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
	completionID := fmt.Sprintf("chatcmpl-gemini-%d", time.Now().UnixNano())
	if parsed.Stream {
		return streamGeminiToOpenAI(writer, upstreamResponse, completionID, parsed.Model)
	}

	var geminiResult geminiResponse
	if err := json.NewDecoder(upstreamResponse.Body).Decode(&geminiResult); err != nil {
		return attemptResult{outcome: outcomeFailed, err: fmt.Errorf("decode gemini response: %w", err)}
	}

	responseText, finishReason := flattenGeminiCandidates(geminiResult.Candidates)
	promptTokens := geminiResult.UsageMetadata.PromptTokenCount
	completionTokens := geminiResult.UsageMetadata.CandidatesTokenCount
	responseBody := chatCompletionResponse{
		ID:      completionID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   parsed.Model,
		Choices: []chatChoiceItem{{
			Index:        0,
			Message:      &chatFullMessage{Role: "assistant", Content: responseText},
			FinishReason: finishReason,
		}},
		Usage: &chatUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
	responseBytes := writeJSONResponseWithBody(writer, responseBody)
	return attemptResult{
		outcome:          outcomeServed,
		body:             responseBytes,
		status:           http.StatusOK,
		contentType:      "application/json",
		promptTokens:     promptTokens,
		completionTokens: completionTokens,
	}
}

func buildGeminiRequest(request chatRequest) *geminiRequest {
	result := &geminiRequest{}
	// Only forward a token cap the client actually asked for. Gemini does not
	// require maxOutputTokens and defaults to the model's own limit, so
	// substituting Anthropic's mandatory-field default here would silently
	// truncate generations at 4096 tokens. omitempty drops the zero value.
	generationConfig := &geminiGenConfig{
		Temperature:     request.Temperature,
		TopP:            request.TopP,
		MaxOutputTokens: request.requestedMaxTokens(),
	}
	switch stops := request.Stop.(type) {
	case string:
		if stops != "" {
			generationConfig.StopSequences = []string{stops}
		}
	case []any:
		for _, item := range stops {
			if text, ok := item.(string); ok {
				generationConfig.StopSequences = append(generationConfig.StopSequences, text)
			}
		}
	}
	result.GenerationConfig = generationConfig

	for _, message := range request.Messages {
		text := strings.TrimSpace(messageText(message.Content))
		if text == "" {
			continue
		}
		switch message.Role {
		case "system", "developer":
			if result.SystemInstruction == nil {
				result.SystemInstruction = &geminiContent{}
			}
			result.SystemInstruction.Parts = append(result.SystemInstruction.Parts, geminiPart{Text: text})
		default:
			appendGeminiContent(result, message.Role, text)
		}
	}
	return result
}

// appendGeminiContent merges adjacent messages with the same Gemini role,
// since generateContent expects alternating user/model turns.
func appendGeminiContent(result *geminiRequest, role, text string) {
	geminiRole := "user"
	if role == "assistant" {
		geminiRole = "model"
	}
	lastIndex := len(result.Contents) - 1
	if lastIndex >= 0 && result.Contents[lastIndex].Role == geminiRole {
		result.Contents[lastIndex].Parts = append(result.Contents[lastIndex].Parts, geminiPart{Text: text})
		return
	}
	result.Contents = append(result.Contents, geminiContent{
		Role:  geminiRole,
		Parts: []geminiPart{{Text: text}},
	})
}

func flattenGeminiCandidates(candidates []geminiCandidate) (string, any) {
	text := ""
	finishReason := any("stop")
	for _, candidate := range candidates {
		for _, part := range candidate.Content.Parts {
			text += part.Text
		}
		switch candidate.FinishReason {
		case "MAX_TOKENS":
			finishReason = "length"
		case "", "STOP":
			// keep default "stop"
		default:
			finishReason = candidate.FinishReason
		}
	}
	return text, finishReason
}

func streamGeminiToOpenAI(
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

	sendChunk(&chatChoiceDelta{Role: "assistant"}, nil)

	scanner := bufio.NewScanner(upstreamResponse.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	finalFinishReason := any(nil)

	// Gemini reports usageMetadata on the final chunk of a stream.
	var promptTokens, completionTokens int

	for scanner.Scan() {
		payload, ok := sseData(scanner.Text())
		if !ok {
			continue
		}
		var chunk geminiResponse
		if json.Unmarshal(payload, &chunk) != nil {
			continue
		}
		text, finishReason := flattenGeminiCandidates(chunk.Candidates)
		if text != "" {
			if !sendChunk(&chatChoiceDelta{Content: text}, nil) {
				return attemptResult{outcome: outcomeServed, promptTokens: promptTokens, completionTokens: completionTokens}
			}
		}
		if textValue, isString := finishReason.(string); isString {
			finalFinishReason = textValue
		}
		if chunk.UsageMetadata.PromptTokenCount > 0 || chunk.UsageMetadata.CandidatesTokenCount > 0 {
			promptTokens = chunk.UsageMetadata.PromptTokenCount
			completionTokens = chunk.UsageMetadata.CandidatesTokenCount
		}
	}

	sendChunk(&chatChoiceDelta{}, finalFinishReason)
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
