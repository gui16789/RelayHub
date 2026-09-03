package relay

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/local/relayhub/internal/config"
	"github.com/local/relayhub/internal/stats"
)

// Cooldown durations applied when an upstream rejects a specific key. The
// values come from config.BuiltinCooldowns() so an unconfigured channel
// behaves exactly like the historical constants; per-channel/server
// cooldowns override them at request time (see attempt.cooldowns).
var (
	authCooldown  = config.BuiltinCooldowns().Auth.D()      // 401/403: key is bad or revoked
	quotaCooldown = config.BuiltinCooldowns().RateLimit.D() // 429: rate limited, retry soon
	// maxRetryAfter caps an upstream Retry-After hint so a hostile or buggy
	// "retry in 24h" cannot pin a key out of rotation for a day.
	maxRetryAfter = config.BuiltinCooldowns().MaxRetryAfter.D()
)

// maxRequestBody caps incoming client request bodies (32 MiB).
const maxRequestBody = 32 << 20

// ConfigSource abstracts the live config so the handler always sees the
// latest channels and the master enable switch.
type ConfigSource interface {
	Snapshot() *config.Config
	IsEnabled() bool
}

type Handler struct {
	source ConfigSource
	state  *State
	stats  *stats.Collector
	// client serves buffered (non-streaming) requests and carries a total
	// timeout; streamClient serves SSE streams and has NO total timeout so
	// a long generation is never cut off mid-stream (client disconnects
	// still cancel via the request context).
	client       *http.Client
	streamClient *http.Client

	// healthProbe is the injected upstream checker used by the background
	// health loop (see probe_loop.go). probeMu guards healthProbe and the
	// probeStop channel; probeStop is non-nil while the loop runs.
	probeMu     sync.Mutex
	healthProbe HealthProbeFunc
	probeStop   chan struct{}

	// breaker prevents a single request from exhausting all keys in a
	// channel during rate limit storms.
	breaker *CircuitBreaker

	// cache stores responses for identical requests to reduce upstream load.
	cache       *ResponseCache
	cacheConfig CacheConfig
}

func NewHandler(source ConfigSource, collector *stats.Collector) *Handler {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 5 * time.Second,
	}
	h := &Handler{
		source:      source,
		state:       NewState(),
		stats:       collector,
		breaker:     NewCircuitBreaker(),
		cache:       NewResponseCache(),
		cacheConfig: DefaultCacheConfig(),
		client:      &http.Client{Transport: transport, Timeout: 5 * time.Minute},
		// ResponseHeaderTimeout only: an upstream that never starts
		// answering still gives up, but an open stream may run for hours.
		streamClient: &http.Client{Transport: transport, Timeout: 0},
	}
	// Start background cache eviction (every 5 minutes)
	go h.cacheEvictionLoop()
	return h
}

// cacheEvictionLoop removes expired cache entries periodically.
func (h *Handler) cacheEvictionLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.cache.Evict()
	}
}

// State exposes key cooldowns for the admin console.
func (h *Handler) State() *State { return h.state }

// ServeHTTP proxies chat completions requests with priority routing,
// round-robin key rotation and automatic failover.
func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !h.source.IsEnabled() {
		http.Error(writer, "proxy is disabled by admin", http.StatusServiceUnavailable)
		return
	}

	// Cap the incoming body so a broken or hostile client cannot exhaust
	// memory with a multi-GB payload (LLM requests are far below this).
	request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBody)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			http.Error(writer, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(writer, "read request body failed: "+err.Error(), http.StatusBadRequest)
		return
	}

	var parsed chatRequest
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Model == "" {
		http.Error(writer, "invalid chat completion request: missing model", http.StatusBadRequest)
		return
	}

	// Determine if this request is cacheable and get the appropriate TTL.
	endpoint := request.URL.Path
	cacheTTL := h.getCacheTTL(endpoint, &parsed)

	// Try cache first if this endpoint is cacheable.
	if cacheTTL > 0 {
		if cached, ok := h.cache.Get(endpoint, parsed.Model, body); ok {
			// Cache hit: serve from cache
			writer.Header().Set("Content-Type", cached.contentType)
			writer.Header().Set("X-Cache", "HIT")
			writer.WriteHeader(cached.status)
			writer.Write(cached.response)
			h.stats.RecordRequest()
			h.stats.RecordTokens("cache", cached.promptTokens, cached.completionTokens)
			slog.Debug("cache hit", "endpoint", endpoint, "model", parsed.Model)
			return
		}

		// Check if another goroutine is already fetching this (request coalescing)
		if _, won := h.cache.StartInflight(endpoint, parsed.Model, body); !won {
			// Lost the race: another goroutine is fetching, wait for it
			slog.Debug("request coalescing: waiting for inflight request", "endpoint", endpoint, "model", parsed.Model)
			if cached, ok := h.cache.Get(endpoint, parsed.Model, body); ok {
				// The inflight request completed successfully
				writer.Header().Set("Content-Type", cached.contentType)
				writer.Header().Set("X-Cache", "HIT-COALESCED")
				writer.WriteHeader(cached.status)
				writer.Write(cached.response)
				h.stats.RecordRequest()
				h.stats.RecordTokens("cache", cached.promptTokens, cached.completionTokens)
				slog.Debug("cache hit from coalesced request", "endpoint", endpoint, "model", parsed.Model)
				return
			}
			// Inflight request failed, fall through to normal fetch
		} else {
			// Won the race: we're responsible for fetching and caching
			defer h.cache.FinishInflight(endpoint, parsed.Model, body)
			// Continue to normal fetch logic below, will cache on success
		}
	}

	h.stats.RecordRequest()

	// The trace records this request's full routing path so the console can
	// answer "why did this go to channel X / how many hops did it take".
	routingStart := time.Now()
	var hops []stats.TraceHop
	var servedPrompt, servedCompletion int
	finishTrace := func(finalStatus int, finalChannel string) {
		h.stats.RecordTrace(stats.RequestTrace{
			Time:             time.Now().Format("01-02 15:04:05"),
			Model:            parsed.Model,
			Stream:           parsed.Stream,
			FinalStatus:      finalStatus,
			FinalChannel:     finalChannel,
			TotalMS:          time.Since(routingStart).Milliseconds(),
			PromptTokens:     servedPrompt,
			CompletionTokens: servedCompletion,
			Hops:             hops,
		})
	}

	snapshot := h.source.Snapshot()
	// Only OpenAI-type channels speak non-chat protocols natively: sending
	// an embeddings/images/responses payload through the anthropic/gemini
	// chat converters would produce garbage.
	openAIOnly := request.URL.Path != "/v1/chat/completions"
	// Likewise for chat requests using features the converters cannot express
	// (tools, vision parts, response_format, n>1). Restricting them to
	// passthrough channels keeps the request honest: it either reaches an
	// upstream that truly supports the feature, or it fails with a message
	// naming what is unsupported, instead of returning 200 and a reply that
	// quietly ignored half the request.
	unsupported := parsed.unsupportedFeatures()
	if len(unsupported) > 0 {
		openAIOnly = true
	}
	attempts := h.buildAttempts(snapshot, parsed.Model, openAIOnly)
	if max := snapshot.Server.MaxAttempts; max > 0 && len(attempts) > max {
		slog.Info("attempts capped", "model", parsed.Model, "candidates", len(attempts), "max", max)
		attempts = attempts[:max]
	}
	if len(attempts) == 0 {
		candidates := snapshot.CandidateChannels(parsed.Model)
		if len(candidates) == 0 {
			// Genuinely no enabled channel declares this model: a config gap,
			// not a transient failure, so 404.
			h.stats.RecordUnrouted(parsed.Model)
			finishTrace(http.StatusNotFound, "")
			http.Error(writer, fmt.Sprintf("no channel serves model %q", parsed.Model), http.StatusNotFound)
			return
		}
		// Channels serve this model, but the request needs features only a
		// passthrough channel can carry and none of them is one. That is a
		// property of the request, so say so plainly rather than letting it
		// fall through to the retryable 503 below.
		if len(unsupported) > 0 && !hasOpenAIChannel(candidates) {
			features := strings.Join(unsupported, ", ")
			h.stats.RecordUnconvertible(parsed.Model, features)
			finishTrace(http.StatusBadRequest, "")
			http.Error(writer, fmt.Sprintf(
				"request uses %s, which cannot be converted to the %q protocol; "+
					"route model %q through a channel of type openai to use it",
				features, candidates[0].Type, parsed.Model), http.StatusBadRequest)
			return
		}
		// Channels exist for the model but none is usable right now: either
		// every key is cooling down (e.g. right after a wave of 429s / 5xx
		// streaks) or the health probe has marked them unreachable. Both are
		// retryable, so 503 tells clients to back off instead of treating
		// this as a dead model.
		h.stats.RecordTotalFailure(parsed.Model)
		reason := "all keys for model " + parsed.Model + " are cooling down"
		for _, channel := range snapshot.CandidateChannels(parsed.Model) {
			if h.state.IsDown(channel.Name) {
				reason = "all channels for model " + parsed.Model + " are currently marked unreachable by health probes"
				break
			}
		}
		h.stats.PushEvent("warn", "", reason)
		finishTrace(http.StatusServiceUnavailable, "")
		// Tell the client how long to back off: the earliest cooldown among
		// this model's candidate channels is when retrying becomes useful.
		if retryAfter := h.earliestCooldownSeconds(snapshot.CandidateChannels(parsed.Model)); retryAfter > 0 {
			writer.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		http.Error(writer, "no upstream is available right now, retry shortly", http.StatusServiceUnavailable)
		return
	}

	var lastErr error
	for _, attempt := range attempts {
		attemptStart := time.Now()
		result := h.tryAttempt(writer, request, body, attempt)
		hopDetail := errText(result.err)
		if result.outcome == outcomeServed && result.note != "" {
			// The client got a response but something went wrong mid-stream
			// (truncated SSE, scanner overflow): surface it for observability
			// since failover is impossible once headers are sent.
			hopDetail = result.note
			h.stats.PushEvent("warn", attempt.channel.Name, "stream: "+result.note)
			slog.Warn("stream anomaly", "channel", attempt.channel.Name, "note", result.note)
		}
		hops = append(hops, stats.TraceHop{
			Channel:    attempt.channel.Name,
			KeyTail:    tail(attempt.apiKey, 4),
			Status:     result.status,
			Result:     hopResult(result.outcome),
			Detail:     hopDetail,
			DurationMS: time.Since(attemptStart).Milliseconds(),
		})
		switch result.outcome {
		case outcomeServed:
			h.stats.RecordAttempt(attempt.channel.Name, true)
			h.breaker.RecordSuccess(attempt.channel.Name)
			if result.promptTokens > 0 || result.completionTokens > 0 {
				h.stats.RecordTokens(attempt.channel.Name, result.promptTokens, result.completionTokens)
			}
			servedPrompt, servedCompletion = result.promptTokens, result.completionTokens

			// Cache the successful response if cacheable
			if cacheTTL > 0 && !parsed.Stream {
				h.cache.Set(endpoint, parsed.Model, body, result.body, result.status,
					result.contentType, result.promptTokens, result.completionTokens, cacheTTL)
				slog.Debug("cached response", "endpoint", endpoint, "model", parsed.Model, "ttl", cacheTTL)
			}

			finishTrace(http.StatusOK, attempt.channel.Name)
			return
		case outcomeAborted:
			// The upstream rejected the payload (e.g. 400): no failover is
			// possible, but the request ends in the failed total and the
			// upstream error is echoed back instead of an empty response.
			h.stats.RecordRejected(attempt.channel.Name)
			h.writeUpstreamError(writer, result)
			status := result.status
			if status == 0 {
				status = http.StatusBadRequest
			}
			finishTrace(status, attempt.channel.Name)
			return
		case outcomeFailed:
			lastErr = result.err
			h.stats.RecordAttempt(attempt.channel.Name, false)
			// If this is a rate limit or server error, record it for circuit breaking.
			if result.status == http.StatusTooManyRequests || result.status >= 500 {
				if h.breaker.RecordFailure(attempt.channel.Name) {
					h.stats.PushEvent("warn", attempt.channel.Name,
						"circuit breaker tripped after rate limit storm, cooling down 15s")
					slog.Warn("circuit breaker tripped", "channel", attempt.channel.Name)
				}
			}
			h.stats.PushEvent("warn", attempt.channel.Name, "failover: "+errText(result.err))
			slog.Warn("failover", "channel", attempt.channel.Name, "key_tail", tail(attempt.apiKey, 4), "err", result.err)
		}
	}
	h.stats.RecordTotalFailure(parsed.Model)
	slog.Error("all attempts failed", "model", parsed.Model, "attempts", len(attempts))
	finishTrace(http.StatusBadGateway, "")
	http.Error(writer, "all upstream attempts failed, last error: "+errText(lastErr), http.StatusBadGateway)
}

// hasOpenAIChannel reports whether any candidate passes requests through
// verbatim, which is what an unconvertible feature requires.
func hasOpenAIChannel(channels []config.Channel) bool {
	for _, channel := range channels {
		if channel.Type == config.TypeOpenAI {
			return true
		}
	}
	return false
}

func hopResult(outcome outcomeKind) string {
	switch outcome {
	case outcomeServed:
		return "served"
	case outcomeAborted:
		return "aborted"
	default:
		return "failed"
	}
}

type attempt struct {
	channel config.Channel
	apiKey  string
	// upstreamModel is the model name this channel's upstream actually uses
	// (after the channel's model_map translation); clientModel is what the
	// caller requested, kept so the response can echo the client's name back.
	upstreamModel string
	clientModel   string
	// cooldowns are the fully-resolved cool-down durations for this
	// channel (channel override → server override → built-in defaults).
	cooldowns config.Cooldowns
}

// buildAttempts expands every candidate channel into per-key attempts,
// honoring priority order and round-robin key rotation. Channels the
// background health probe has marked down are skipped: a freshly-probed
// dead upstream must not absorb one failed attempt per request. Channels
// with a tripped circuit breaker are also skipped to prevent exhausting
// all keys during rate limit storms.
func (h *Handler) buildAttempts(snapshot *config.Config, model string, openAIOnly bool) []attempt {
	var attempts []attempt
	for _, channel := range snapshot.CandidateChannels(model) {
		if h.state.IsDown(channel.Name) {
			continue
		}
		// Skip channels with a tripped circuit breaker: they just hit a
		// wave of 429s/5xx and need time to recover.
		if h.breaker.IsTripped(channel.Name) {
			slog.Debug("circuit breaker tripped, skipping channel", "channel", channel.Name)
			continue
		}
		if openAIOnly && channel.Type != config.TypeOpenAI {
			continue
		}
		upstreamModel := channel.UpstreamModel(model)
		cooldowns := config.EffectiveCooldowns(snapshot.Server.Cooldowns, channel.Cooldowns)
		for _, apiKey := range h.state.OrderedKeys(channel, snapshot.Server.KeyStrategy) {
			attempts = append(attempts, attempt{
				channel:       channel,
				apiKey:        apiKey,
				upstreamModel: upstreamModel,
				clientModel:   model,
				cooldowns:     cooldowns,
			})
		}
	}
	return attempts
}

type outcomeKind int

const (
	outcomeFailed  outcomeKind = iota // try next attempt
	outcomeServed                     // response fully written to client
	outcomeAborted                    // do not fail over (client error)
)

type attemptResult struct {
	outcome outcomeKind
	err     error
	// status and body carry the upstream error through for the aborted
	// (client-error) path so it can be echoed back to the caller instead of
	// leaving the response empty.
	status      int
	body        []byte
	contentType string
	// promptTokens/completionTokens are the upstream-reported usage of a
	// served request; zero means the upstream did not report it.
	promptTokens     int
	completionTokens int
	// note describes an anomaly while serving (e.g. the upstream closed a
	// stream early). The attempt still counts as served: headers were sent
	// and failover is impossible, but the event feed and trace record it.
	note string
}

// handleUpstreamError is the single place that decides what a non-200
// upstream response means for the routing loop: failover or abort, and
// whether the offending key gets cooled down. 5xx additionally feeds the
// error-aware streak; a key that trips the threshold is rested briefly so a
// dead upstream stops absorbing every request before failover kicks in.
func (h *Handler) handleUpstreamError(response *http.Response, attempt attempt) attemptResult {
	reason := readErrorBody(response)
	cd := attempt.cooldowns
	outcome, cooldown, message := classifyUpstream(response, reason, cd)
	if cooldown == cooldownUseQuotaBackoff {
		applied := h.state.PenalizeQuota(
			attempt.channel.Name, attempt.apiKey,
			quotaResetHint(response, reason, cd.QuotaMax.D()),
			cd.QuotaBase.D(), cd.QuotaMax.D())
		h.stats.PushEvent("warn", attempt.channel.Name,
			fmt.Sprintf("key ...%s quota exhausted, parked for %s", tail(attempt.apiKey, 4), applied.Round(time.Second)))
	} else if cooldown > 0 {
		h.coolDown(attempt, cooldown)
	} else if response.StatusCode >= 500 {
		if streak := h.state.MarkServerError(attempt.channel.Name, attempt.apiKey); streak == serverStreakThreshold {
			h.coolDown(attempt, serverCooldown)
			h.stats.PushEvent("warn", attempt.channel.Name,
				fmt.Sprintf("key ...%s cooling down %s after %d consecutive 5xx", tail(attempt.apiKey, 4), serverCooldown, streak))
		}
	}
	return attemptResult{
		outcome: outcome,
		err:     fmt.Errorf("%s: %s", message, reason),
		status:  response.StatusCode,
		body:    []byte(reason),
	}
}

// handleAttemptError converts a pre-response network failure into an
// attempt result (always failover, no cooldown: a local network blip must
// not park a good key).
func handleAttemptError(err error, _ attempt) attemptResult {
	return attemptResult{
		outcome: outcomeFailed,
		err:     fmt.Errorf("upstream unreachable: %w", err),
	}
}

// writeUpstreamError echoes a rejected upstream response back to the client
// so a 4xx from upstream does not surface as an empty 200.
func (h *Handler) writeUpstreamError(writer http.ResponseWriter, result attemptResult) {
	status := result.status
	if status == 0 {
		status = http.StatusBadRequest
	}
	payload := result.body
	if len(payload) == 0 {
		payload = []byte(`{"error":{"message":"` + errText(result.err) + `"}}`)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(payload)
}

func (h *Handler) tryAttempt(
	writer http.ResponseWriter,
	request *http.Request,
	body []byte,
	attempt attempt,
) (result attemptResult) {
	defer func() {
		// A panic mid-stream must not take the whole server down.
		if recovered := recover(); recovered != nil {
			h.coolDown(attempt, time.Minute)
			slog.Error("panic in attempt", "channel", attempt.channel.Name, "recovered", recovered)
			result = attemptResult{outcome: outcomeFailed, err: fmt.Errorf("panic: %v", recovered)}
		}
	}()

	switch attempt.channel.Type {
	case config.TypeOpenAI:
		return h.relayOpenAI(writer, request, body, attempt)
	case config.TypeAnthropic:
		return h.relayAnthropic(writer, request, body, attempt)
	case config.TypeGemini:
		return h.relayGemini(writer, request, body, attempt)
	default:
		return attemptResult{outcome: outcomeFailed, err: fmt.Errorf("unknown channel type %q", attempt.channel.Type)}
	}
}

// coolDown parks a misbehaving key and records it for the admin console.
func (h *Handler) coolDown(attempt attempt, duration time.Duration) {
	h.state.Penalize(attempt.channel.Name, attempt.apiKey, duration)
	h.stats.PushEvent("warn", attempt.channel.Name,
		fmt.Sprintf("key ...%s cooling down for %s", tail(attempt.apiKey, 4), duration))
}

// earliestCooldownSeconds returns the smallest remaining cooldown (whole
// seconds, rounded up) across keys of the given channels; 0 when nothing
// is cooling down.
func (h *Handler) earliestCooldownSeconds(channels []config.Channel) int {
	names := make(map[string]bool, len(channels))
	for _, channel := range channels {
		names[channel.Name] = true
	}
	var minMS int64 = -1
	for _, info := range h.state.Cooldowns() {
		if !names[info.Channel] {
			continue
		}
		if minMS < 0 || info.RemainMS < minMS {
			minMS = info.RemainMS
		}
	}
	if minMS <= 0 {
		return 0
	}
	return int((minMS + 999) / 1000)
}

// cooldownUseQuotaBackoff is the sentinel classifyUpstream returns for a
// quota-exhaustion 429: the caller must park the key via PenalizeQuota so
// the strike count drives the backoff ladder (and persistence), instead of
// applying a fixed-duration cooldown.
const cooldownUseQuotaBackoff = time.Duration(-1)

// classifyUpstream decides what to do with a non-200 upstream response and
// how long to cool the offending key down. For 429 it distinguishes a
// transient rate limit (honor Retry-After / short default) from quota
// exhaustion (backoff ladder) by inspecting the error body. Cooldown
// durations come from the attempt's resolved channel config.
func classifyUpstream(response *http.Response, body string, cd config.Cooldowns) (outcomeKind, time.Duration, string) {
	switch response.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return outcomeFailed, cd.Auth.D(), fmt.Sprintf("upstream auth rejected (%d)", response.StatusCode)
	case http.StatusTooManyRequests:
		if isQuotaExhausted(response, body) {
			return outcomeFailed, cooldownUseQuotaBackoff, "upstream quota exhausted (429)"
		}
		cooldown := retryAfterDuration(response, cd)
		message := "upstream rate limited (429)"
		if cooldown > 0 {
			message += fmt.Sprintf(", retry after %s", cooldown.Round(time.Second))
		}
		return outcomeFailed, cooldown, message
	case http.StatusBadRequest:
		return outcomeAborted, 0, "bad request"
	default:
		return outcomeFailed, 0, fmt.Sprintf("unexpected upstream status (%d)", response.StatusCode)
	}
}

// rateExhaustedKeywords are transient window-exhaustion phrases (RPS/QPS/
// RPM) whose window refills in seconds. They must be checked BEFORE quota
// markers because some upstreams (SenseNova) tag these errors with a
// "quota_exceeded_error" type even though the RPS bucket recovers almost
// immediately — a long quota parking would waste the key.
var rateExhaustedKeywords = []string{
	"rps exhausted", "qps exhausted", "rpm exhausted",
	"requests exhausted", "request rate exhausted",
}

// quotaKeywords mark a 429 as quota exhaustion (a billing/token-per-minute
// window that must refill) rather than a transient rate limit. SenseNova
// reports TPM exhaustion as code ModelAccountTpmRateLimitExceeded with body
// "inference tpm exhausted" — matched via the specific phrases below, since a
// bare "tpm" substring appears in both quota and transient rate-limit
// wording. OpenAI-style upstreams use codes such as insufficient_quota.
var quotaKeywords = []string{
	"quota", "额度", "insufficient_quota", "billing", "usage limit",
	"daily limit", "monthly limit", "resource_exhausted",
	// SenseNova: ModelAccountTpmRateLimitExceeded / "inference tpm exhausted".
	"modelaccount", "tpm exhausted", "tokens per minute",
}

// rateKeywords mark a 429 as a transient rate limit (QPS/RPM/TPM), checked
// AFTER quota keywords: an explicit quota/exhaustion marker must win — e.g.
// SenseNova's ModelAccountTpmRateLimitExceeded would otherwise trip the
// "ratelimit" substring inside its own error code.
var rateKeywords = []string{
	"rate limit", "ratelimit", "qps", "rpm", "tpm", "concurrent", "too many requests",
}

// isQuotaExhausted classifies a 429 body. A transient window-exhaustion
// phrase (rps/qps exhausted) wins immediately: those refill in seconds.
// Otherwise an explicit quota/exhaustion marker wins (the window must
// refill, so the key is parked on the backoff ladder rather than retried
// after a short cooldown); an explicit rate-limit marker without one means
// transient; an unclassified 429 stays a (short) rate limit so we keep
// retrying soon.
func isQuotaExhausted(response *http.Response, body string) bool {
	lower := strings.ToLower(body)
	for _, keyword := range rateExhaustedKeywords {
		if strings.Contains(lower, keyword) {
			return false
		}
	}
	for _, keyword := range quotaKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	for _, keyword := range rateKeywords {
		if strings.Contains(lower, keyword) {
			return false
		}
	}
	return false
}

// quotaResetHint extracts when the exhausted quota window refills, so the
// key can be parked exactly until then instead of guessing with backoff.
// Sources, in order: dedicated reset headers (epoch seconds or a duration),
// a reset timestamp inside the JSON error body. 0 means "unknown".
// maxCooldown caps the returned duration so a reset hint pointing weeks out
// cannot pin the key beyond the channel's quota ceiling.
func quotaResetHint(response *http.Response, body string, maxCooldown time.Duration) time.Duration {
	for _, name := range []string{
		"X-Quota-Reset", "X-Ratelimit-Reset-Quota", "X-Ratelimit-Reset",
		"X-Ratelimit-Reset-Requests", "Quota-Reset",
	} {
		if duration, ok := parseResetValue(response.Header.Get(name), maxCooldown); ok {
			return duration
		}
	}

	// JSON body fields some upstreams carry, e.g.
	// {"error":{"reset_at":173...}} or Gemini-style quotaResetTimeStamp.
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return 0
	}
	for _, container := range []any{payload, payload["error"]} {
		object, ok := container.(map[string]any)
		if !ok {
			continue
		}
		for _, field := range []string{"reset_at", "resetAt", "quotaResetTimeStamp", "quota_reset_at"} {
			if duration, ok := parseResetAny(object[field], maxCooldown); ok {
				return duration
			}
		}
	}
	return 0
}

// parseResetAny accepts a JSON number or numeric string.
func parseResetAny(value any, maxCooldown time.Duration) (time.Duration, bool) {
	switch typed := value.(type) {
	case float64:
		return parseResetNumber(typed, maxCooldown)
	case string:
		return parseResetValue(typed, maxCooldown)
	}
	return 0, false
}

// parseResetValue interprets a reset hint: an epoch timestamp (seconds or
// milliseconds) becomes time.Until, a small number is treated as a
// duration in seconds (capped at maxCooldown like the quota ladder).
func parseResetValue(raw string, maxCooldown time.Duration) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if when, err := http.ParseTime(raw); err == nil {
		if until := time.Until(when); until > 0 {
			return min(until, maxCooldown), true
		}
		return 0, false
	}
	number, err := strconv.ParseFloat(raw, 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return parseResetNumber(number, maxCooldown)
}

func parseResetNumber(number float64, maxCooldown time.Duration) (time.Duration, bool) {
	switch {
	case number > 1e12: // epoch milliseconds
		if until := time.Until(time.UnixMilli(int64(number))); until > 0 {
			return min(until, maxCooldown), true
		}
	case number > 1e9: // epoch seconds
		if until := time.Until(time.Unix(int64(number), 0)); until > 0 {
			return min(until, maxCooldown), true
		}
	case number < float64(maxCooldown/time.Second):
		return time.Duration(number * float64(time.Second)), true
	}
	return 0, false
}

// retryAfterDuration parses the Retry-After header (delta-seconds or
// HTTP-date, per RFC 9110) into a cooldown. Missing, invalid or out-of-range
// values fall back to the channel's rate-limit default so a bad header can
// never pin a key out of rotation indefinitely.
func retryAfterDuration(response *http.Response, cd config.Cooldowns) time.Duration {
	rateLimit := cd.RateLimit.D()
	maxRetryAfter := cd.MaxRetryAfter.D()
	header := response.Header.Get("Retry-After")
	if header == "" {
		return rateLimit
	}

	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return rateLimit
		}
		cooldown := time.Duration(seconds) * time.Second
		if cooldown > maxRetryAfter {
			cooldown = maxRetryAfter
		}
		return cooldown
	}

	if when, err := http.ParseTime(header); err == nil {
		cooldown := time.Until(when)
		if cooldown <= 0 {
			return rateLimit
		}
		if cooldown > maxRetryAfter {
			cooldown = maxRetryAfter
		}
		return cooldown
	}

	return rateLimit
}

func readErrorBody(response *http.Response) string {
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4*1024))
	if len(raw) == 0 {
		return "(empty body)"
	}
	return strings.TrimSpace(string(raw))
}

func tail(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[len(value)-n:]
}

func errText(err error) string {
	if err == nil {
		return "unknown"
	}
	return strings.TrimSpace(err.Error())
}
