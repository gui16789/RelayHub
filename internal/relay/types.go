package relay

import (
	"encoding/json"
	"sort"
	"strings"
)

// Minimal OpenAI-compatible chat types shared by converters.

type chatRequest struct {
	Model    string        `json:"model"`
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

	// The fields below are NOT translated by the anthropic/gemini converters.
	// They are captured only so unsupportedFeatures can detect them and steer
	// the request to a passthrough (openai) channel, instead of answering as
	// if the client had never asked.
	Tools          json.RawMessage `json:"tools,omitempty"`
	ToolChoice     json.RawMessage `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	N              *int            `json:"n,omitempty"`
}

// anthropicDefaultMaxTokens applies only to Anthropic, whose API rejects a
// request that omits max_tokens. Gemini defaults to the model's own limit, so
// imposing this number there would silently truncate a generation the client
// never capped.
const anthropicDefaultMaxTokens = 4096

// requestedMaxTokens returns the client's explicit token limit, or 0 when it
// did not set one.
func (r *chatRequest) requestedMaxTokens() int {
	if r.MaxCompletionToken != nil && *r.MaxCompletionToken > 0 {
		return *r.MaxCompletionToken
	}
	if r.MaxTokens != nil && *r.MaxTokens > 0 {
		return *r.MaxTokens
	}
	return 0
}

// effectiveMaxTokens is the Anthropic-facing limit: the client's value, or the
// default that satisfies Anthropic's mandatory field.
func (r *chatRequest) effectiveMaxTokens() int {
	if requested := r.requestedMaxTokens(); requested > 0 {
		return requested
	}
	return anthropicDefaultMaxTokens
}

// unsupportedFeatures names request features the anthropic/gemini converters
// cannot represent. Those converters rebuild the upstream payload from a
// text-only model, so anything listed here would otherwise vanish silently:
// the client would get HTTP 200 and a reply that ignored what it asked for
// (no tool_calls, prose instead of JSON, a missing image).
//
// An empty result means any channel type can serve the request.
func (r *chatRequest) unsupportedFeatures() []string {
	var features []string
	if !isJSONEmpty(r.Tools) {
		features = append(features, "tools")
	}
	if !isJSONEmpty(r.ToolChoice) {
		features = append(features, "tool_choice")
	}
	// response_format only matters when it constrains the output; the explicit
	// {"type":"text"} form is what the converters already produce.
	if format := responseFormatType(r.ResponseFormat); format != "" && format != "text" {
		features = append(features, "response_format="+format)
	}
	if r.N != nil && *r.N > 1 {
		features = append(features, "n>1")
	}
	// Non-text content parts (images, audio, files) are flattened away by
	// messageText, so a vision request would lose the very part it is about.
	features = append(features, nonTextContentKinds(r.Messages)...)
	return features
}

// isJSONEmpty reports whether a raw JSON value carries no information, so an
// explicitly-null or empty field is not mistaken for a requested feature.
func isJSONEmpty(raw json.RawMessage) bool {
	switch strings.TrimSpace(string(raw)) {
	case "", "null", "[]", "{}":
		return true
	}
	return false
}

func responseFormatType(raw json.RawMessage) string {
	if isJSONEmpty(raw) {
		return ""
	}
	var format struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &format); err != nil {
		return ""
	}
	return format.Type
}

// nonTextContentKinds lists the distinct non-text part types across the
// messages, e.g. "image_url". Sorted so error messages are deterministic.
func nonTextContentKinds(messages []chatMessage) []string {
	var kinds []string
	seen := map[string]bool{}
	for _, message := range messages {
		var parts []contentPart
		if err := json.Unmarshal(message.Content, &parts); err != nil {
			continue
		}
		for _, part := range parts {
			if part.Type == "" || part.Type == "text" || seen[part.Type] {
				continue
			}
			seen[part.Type] = true
			kinds = append(kinds, part.Type)
		}
	}
	sort.Strings(kinds)
	return kinds
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
