package relay

import (
	"bytes"
	"io"
	"net/http"
	"strings"
)

// relayOpenAI forwards the request verbatim to an OpenAI-compatible upstream,
// only swapping the Authorization header. Streaming is a line-by-line copy so
// the final chunk's usage can be observed.
func (h *Handler) relayOpenAI(
	writer http.ResponseWriter,
	clientRequest *http.Request,
	body []byte,
	attempt attempt,
) attemptResult {
	// Ask the upstream to include usage in the final chunk of a stream;
	// without it OpenAI-compatible streams never report tokens. Only the
	// chat endpoint knows stream_options; images/responses bodies differ.
	injected := false
	if clientRequest.URL.Path == "/v1/chat/completions" {
		sentBody, didInject, _ := maybeInjectIncludeUsage(body)
		if didInject {
			body = sentBody
			injected = true
		}
	}
	// Translate the client-facing model name into this channel's upstream
	// name when the channel declares a model_map.
	body = rewriteModel(body, attempt.upstreamModel)

	upstreamURL := attempt.channel.BaseURL + clientRequest.URL.RequestURI()

	upstreamRequest, err := http.NewRequestWithContext(
		clientRequest.Context(), clientRequest.Method, upstreamURL, bytes.NewReader(body))
	if err != nil {
		return attemptResult{outcome: outcomeFailed, err: err}
	}
	copyForwardHeaders(upstreamRequest.Header, clientRequest.Header)
	upstreamRequest.Header.Set("Authorization", "Bearer "+attempt.apiKey)
	applyChannelHeaders(upstreamRequest.Header, attempt.channel.Headers)

	return h.executeAndCopy(writer, upstreamRequest, attempt, injected)
}

// executeAndCopy sends the prepared request; on success it streams the
// upstream response straight back to the client, capturing usage along the
// way (final-chunk scan for streams, body tail parse for plain responses).
// When the channel remapped the model name (model_map), the response's model
// field is rewritten back to the client-facing name so the client never sees
// the upstream alias.
func (h *Handler) executeAndCopy(writer http.ResponseWriter, upstreamRequest *http.Request, attempt attempt, isStream bool) attemptResult {
	client := h.client
	if isStream {
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

	// Restore the client-facing model name when the channel remapped it.
	modelOverride := ""
	if attempt.clientModel != "" && attempt.upstreamModel != "" && attempt.clientModel != attempt.upstreamModel {
		modelOverride = attempt.clientModel
	}

	if !isStream && !isEventStream(upstreamResponse.Header.Get("Content-Type")) {
		return h.copyBufferedWithUsage(writer, upstreamResponse, modelOverride)
	}
	return h.copyStreamWithUsage(writer, upstreamResponse, modelOverride)
}

// copyBufferedWithUsage copies a non-streaming response while parsing its
// trailing usage field. Bodies beyond maxBufferedResponse are streamed
// through without accounting (huge single responses are rare and the tail
// is where usage lives, so only the tail is kept). When modelOverride is
// non-empty the response's model field is rewritten back to it.
func (h *Handler) copyBufferedWithUsage(writer http.ResponseWriter, upstreamResponse *http.Response, modelOverride string) attemptResult {
	copyResponseHeaders(writer.Header(), upstreamResponse.Header)
	writer.WriteHeader(upstreamResponse.StatusCode)

	if upstreamResponse.ContentLength > maxBufferedResponse {
		_, _ = io.Copy(writer, upstreamResponse.Body)
		return attemptResult{outcome: outcomeServed}
	}

	raw, readErr := io.ReadAll(io.LimitReader(upstreamResponse.Body, maxBufferedResponse+1))
	if readErr != nil {
		return attemptResult{outcome: outcomeServed}
	}
	prompt, completion := usageFromOpenAIJSON(raw)
	if modelOverride != "" {
		raw = rewriteModelField(raw, modelOverride)
	}
	if _, writeErr := writer.Write(raw); writeErr != nil {
		return attemptResult{outcome: outcomeServed}
	}
	return attemptResult{
		outcome:          outcomeServed,
		promptTokens:     prompt,
		completionTokens: completion,
	}
}

// copyStreamWithUsage copies an SSE stream line by line and returns the
// usage reported in the final chunk. When modelOverride is non-empty each
// chunk's model field is rewritten back to the client-facing name.
func (h *Handler) copyStreamWithUsage(writer http.ResponseWriter, upstreamResponse *http.Response, modelOverride string) attemptResult {
	copyResponseHeaders(writer.Header(), upstreamResponse.Header)
	writer.WriteHeader(upstreamResponse.StatusCode)

	prompt, completion, streamErr := scanOpenAIStream(writer, upstreamResponse.Body, modelOverride)
	result := attemptResult{
		outcome:          outcomeServed,
		promptTokens:     prompt,
		completionTokens: completion,
	}
	if streamErr != nil {
		result.note = streamErr.Error()
	}
	return result
}

func isEventStream(contentType string) bool {
	return strings.Contains(contentType, "text/event-stream")
}

func copyForwardHeaders(destination, source http.Header) {
	for _, name := range []string{"Content-Type", "Accept", "User-Agent"} {
		if value := source.Get(name); value != "" {
			destination.Set(name, value)
		}
	}
}

// applyChannelHeaders adds the channel's custom headers to the outgoing
// upstream request. It runs AFTER the protocol headers (auth key,
// content-type, anthropic-version) so channel config cannot break the
// request, and the proxy-owned headers are skipped on top of the
// config-level ValidateChannel guard.
func applyChannelHeaders(destination http.Header, headers map[string]string) {
	for name, value := range headers {
		switch strings.ToLower(name) {
		case "authorization", "content-type", "anthropic-version":
			continue
		}
		destination.Set(name, value)
	}
}

func copyResponseHeaders(destination, source http.Header) {
	for _, name := range []string{"Content-Type"} {
		if value := source.Get(name); value != "" {
			destination.Set(name, value)
		}
	}
}
