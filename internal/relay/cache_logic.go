package relay

import "time"

// getCacheTTL returns the cache TTL for a request based on the endpoint and
// request properties. Returns 0 for non-cacheable requests.
func (h *Handler) getCacheTTL(endpoint string, req *chatRequest) time.Duration {
	// Streaming responses cannot be cached (they're consumed as they arrive)
	if req.Stream {
		return 0
	}

	switch endpoint {
	case "/v1/embeddings":
		// Embeddings are deterministic: same input always produces same output
		return h.cacheConfig.EmbeddingsTTL

	case "/v1/moderations":
		// Moderations are also deterministic
		return h.cacheConfig.ModerationsTTL

	case "/v1/chat/completions":
		// Chat completions are only cacheable if temperature=0 (deterministic)
		if req.Temperature != nil && *req.Temperature == 0 {
			return h.cacheConfig.ChatTTL
		}
		// Non-zero temperature = non-deterministic, don't cache
		return 0

	default:
		// Images, audio, responses: not cached (generation endpoints)
		return 0
	}
}
