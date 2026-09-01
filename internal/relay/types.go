package relay

import "encoding/json"

// Minimal OpenAI-compatible chat types shared by converters.

type chatRequest struct {
	Model string `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream,omitempty"`
	// StreamOptions is read (to honor a client that already asked for usage)
	// and injected by the relay on OpenAI-style streams so the upstream
	// reports token usage in the final chunk.
	StreamOptions *struct {
		IncludeUsage *bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`

	// Accept both legacy and newer token-limit field names.
	MaxTokens          *int     `json:"max_tokens,omitempty"`
	MaxCompletionToken *int     `json:"max_completion_tokens,omitempty"`
	Temperature        *float64 `json:"temperature,omitempty"`
	TopP               *float64 `json:"top_p,omitempty"`
	Stop               any      `json:"stop,omitempty"`
}

func (r *chatRequest) effectiveMaxTokens() int {
	const defaultMaxTokens = 4096
	if r.MaxCompletionToken != nil && *r.MaxCompletionToken > 0 {
		return *r.MaxCompletionToken
	}
	if r.MaxTokens != nil && *r.MaxTokens > 0 {
		return *r.MaxTokens
	}
	return defaultMaxTokens
}

type chatMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or array of typed parts
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// messageText flattens either a plain string or a parts array into plain text.
func messageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return plain
	}
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err == nil {
		text := ""
		for _, part := range parts {
			text += part.Text
		}
		return text
	}
	return ""
}

type chatChoiceDelta struct {
	Role         string `json:"role,omitempty"`
	Content      string `json:"content,omitempty"`
	FinishReason any    `json:"finish_reason"`
}

type chatChoiceItem struct {
	Index        int              `json:"index"`
	Delta        *chatChoiceDelta `json:"delta,omitempty"`
	Message      *chatFullMessage `json:"message,omitempty"`
	FinishReason any              `json:"finish_reason"`
}

type chatFullMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatChunk struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []chatChoiceItem `json:"choices"`
	Usage   *chatUsage       `json:"usage,omitempty"` // present in the final chunk when include_usage is on
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type chatCompletionResponse struct {
	ID      string           `json:"id"`
	Object  string           `json:"object"`
	Created int64            `json:"created"`
	Model   string           `json:"model"`
	Choices []chatChoiceItem `json:"choices"`
	Usage   *chatUsage       `json:"usage,omitempty"`
}
