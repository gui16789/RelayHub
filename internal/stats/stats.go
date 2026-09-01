package stats

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// Collector keeps in-memory counters for the admin console. It is purely
// observational: losing it on restart is acceptable.
type Collector struct {
	mu       sync.RWMutex
	startedAt time.Time

	totalRequests         uint64
	totalServed           uint64
	totalFailed           uint64
	totalFallovers        uint64
	totalPromptTokens     uint64
	totalCompletionTokens uint64

	perChannel map[string]*channelCounters
	perModel   map[string]*modelCounters
	// latencies holds recent total request durations (ms) per final channel,
	// used for p50/p95 percentile reporting. Ring buffer, not persisted.
	latencies map[string][]int64

	events  []Event         // ring buffer of recent failover/admin events
	traces  []RequestTrace  // ring buffer of recent request routing traces
}

type channelCounters struct {
	requests         uint64
	served           uint64
	failed           uint64
	promptTokens     uint64
	completionTokens uint64
}

type modelCounters struct {
	requests         uint64
	ok               uint64
	promptTokens     uint64
	completionTokens uint64
	latencySumMS     int64
}

// maxLatencySamples bounds the per-channel latency ring buffer.
const maxLatencySamples = 128

// Event is one log entry shown in the console's event feed.
type Event struct {
	Time    string `json:"time"`
	Level   string `json:"level"` // info | warn | error
	Channel string `json:"channel"`
	Message string `json:"message"`
}

const maxEvents = 200

func NewCollector() *Collector {
	return &Collector{
		startedAt:  time.Now(),
		perChannel: make(map[string]*channelCounters),
		perModel:   make(map[string]*modelCounters),
		latencies:  make(map[string][]int64),
	}
}

// RecordRequest is called once per incoming client request.
func (c *Collector) RecordRequest() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests++
}

// RecordAttempt is called for each upstream attempt: served=true means the
// response was handed to the client, served=false means it failed over.
func (c *Collector) RecordAttempt(channelName string, served bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	counters := c.channelLocked(channelName)
	if served {
		c.totalServed++
		counters.served++
		counters.requests++
	} else {
		c.totalFallovers++
		counters.failed++
		counters.requests++
	}
}

// RecordTokens adds the upstream-reported usage of a served request to the
// channel and global totals (the same fields OpenAI exposes as usage).
func (c *Collector) RecordTokens(channelName string, promptTokens, completionTokens int) {
	if promptTokens <= 0 && completionTokens <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.totalPromptTokens += uint64(promptTokens)
	c.totalCompletionTokens += uint64(completionTokens)
	counters := c.channelLocked(channelName)
	counters.promptTokens += uint64(promptTokens)
	counters.completionTokens += uint64(completionTokens)
}

// RecordRejected counts a request that was answered directly with a client
// error (e.g. the upstream rejected the payload with 400). Unlike a failed
// attempt it is not a failover: no further attempt was made, but the request
// still ends up in the failed total so that
// total_requests == total_served + total_failed always holds.
func (c *Collector) RecordRejected(channelName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.totalFailed++
	counters := c.channelLocked(channelName)
	counters.failed++
	counters.requests++
}

// RecordUnrouted counts a request for which no enabled channel claims the
// model, so the client got a 404 without any upstream attempt.
func (c *Collector) RecordUnrouted(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.totalFailed++
	c.pushEventLocked(Event{
		Time:    nowStamp(),
		Level:   "warn",
		Message: "no channel serves model " + model,
	})
}

// RecordTotalFailure means every attempt failed and the client got an error.
func (c *Collector) RecordTotalFailure(model string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalFailed++
	c.pushEventLocked(Event{
		Time:    nowStamp(),
		Level:   "error",
		Message: "all attempts failed for model " + model,
	})
}

// PushEvent appends a free-form event, e.g. admin actions or key cooldowns.
func (c *Collector) PushEvent(level, channel, message string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pushEventLocked(Event{Time: nowStamp(), Level: level, Channel: channel, Message: message})
}

// TraceHop is one upstream attempt inside a request's routing trace: which
// channel/key was tried, what the upstream answered, and how long it took.
type TraceHop struct {
	Channel    string `json:"channel"`
	KeyTail    string `json:"key_tail"`
	Status     int    `json:"status"`     // upstream status; 0 = network error before a response
	Result     string `json:"result"`     // served | failed | aborted | error
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// RequestTrace records how a single client request was routed end to end.
// The console shows the recent ring so "why did this go to channel X" is
// answerable from the UI instead of the Go log.
type RequestTrace struct {
	Time         string     `json:"time"`
	Model        string     `json:"model"`
	Stream       bool       `json:"stream"`
	FinalStatus  int        `json:"final_status"`  // what the client received
	FinalChannel string     `json:"final_channel"` // "" when no channel served
	TotalMS      int64      `json:"total_ms"`
	PromptTokens int        `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	Hops         []TraceHop `json:"hops"`
}

const maxTraces = 100

// RecordTrace appends a request trace to the ring buffer, and folds it into
// the per-model counters and the per-channel latency ring (the percentile
// source). Traces are the single choke point every completed request flows
// through, which makes them the natural aggregation hook.
func (c *Collector) RecordTrace(trace RequestTrace) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.traces = append(c.traces, trace)
	if len(c.traces) > maxTraces {
		c.traces = c.traces[len(c.traces)-maxTraces:]
	}

	if trace.Model != "" {
		counters, ok := c.perModel[trace.Model]
		if !ok {
			counters = &modelCounters{}
			c.perModel[trace.Model] = counters
		}
		counters.requests++
		if trace.FinalStatus > 0 && trace.FinalStatus < 400 {
			counters.ok++
		}
		counters.promptTokens += uint64(max(trace.PromptTokens, 0))
		counters.completionTokens += uint64(max(trace.CompletionTokens, 0))
		counters.latencySumMS += trace.TotalMS
	}

	if trace.FinalChannel != "" && trace.TotalMS > 0 {
		ring := c.latencies[trace.FinalChannel]
		ring = append(ring, trace.TotalMS)
		if len(ring) > maxLatencySamples {
			ring = ring[len(ring)-maxLatencySamples:]
		}
		c.latencies[trace.FinalChannel] = ring
	}
}

// Traces returns the recent request traces, newest first.
func (c *Collector) Traces(limit int) []RequestTrace {
	c.mu.RLock()
	defer c.mu.RUnlock()

	start := len(c.traces) - limit
	if start < 0 {
		start = 0
	}
	reversed := make([]RequestTrace, 0, len(c.traces)-start)
	for i := len(c.traces) - 1; i >= start; i-- {
		reversed = append(reversed, c.traces[i])
	}
	return reversed
}

func (c *Collector) pushEventLocked(event Event) {
	c.events = append(c.events, event)
	if len(c.events) > maxEvents {
		c.events = c.events[len(c.events)-maxEvents:]
	}
}

// ChannelStat is the per-channel row shown in the console.
type ChannelStat struct {
	Name             string `json:"name"`
	Requests         uint64 `json:"requests"`
	Served           uint64 `json:"served"`
	Failed           uint64 `json:"failed"`
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	// P50MS/P95MS are latency percentiles over the recent request sample
	// (maxLatencySamples); 0 until the channel has served traffic.
	P50MS int64 `json:"p50_ms"`
	P95MS int64 `json:"p95_ms"`
}

// ModelStat is the per-model usage row (LiteLLM-style model leaderboard).
type ModelStat struct {
	Name             string `json:"name"`
	Requests         uint64 `json:"requests"`
	OK               uint64 `json:"ok"`
	PromptTokens     uint64 `json:"prompt_tokens"`
	CompletionTokens uint64 `json:"completion_tokens"`
	AvgMS            int64  `json:"avg_ms"`
}

// maxSummaryModels caps the model leaderboard in the summary payload.
const maxSummaryModels = 20

// percentile returns the p-th percentile of the sample (0..100), or 0 for
// an empty sample. It sorts a copy so the ring stays insertion-ordered.
func percentile(sample []int64, p float64) int64 {
	if len(sample) == 0 {
		return 0
	}
	sorted := make([]int64, len(sample))
	copy(sorted, sample)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	// Nearest-rank method: rank = ceil(p/100 * n), so p50 of [100, 300] is
	// 100 and p95 is 300 (the tail is never understated).
	rank := int(math.Ceil((p / 100) * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// Summary is everything the console needs to render the dashboard header.
type Summary struct {
	Enabled             bool          `json:"enabled"`
	UptimeSeconds       int64         `json:"uptime_seconds"`
	TotalRequests       uint64        `json:"total_requests"`
	TotalServed         uint64        `json:"total_served"`
	TotalFailed         uint64        `json:"total_failed"`
	TotalFallovers      uint64        `json:"total_fallovers"`
	TotalPromptTokens   uint64        `json:"total_prompt_tokens"`
	TotalCompletionTokens uint64      `json:"total_completion_tokens"`
	Channels            []ChannelStat `json:"channels"`
	Models              []ModelStat   `json:"models"`
}

// Summary builds the dashboard payload. enabled comes from the config store,
// passed in to avoid a dependency cycle.
func (c *Collector) Summary(enabled bool) Summary {
	c.mu.RLock()
	defer c.mu.RUnlock()

	channels := make([]ChannelStat, 0, len(c.perChannel))
	for name, counters := range c.perChannel {
		channels = append(channels, ChannelStat{
			Name:             name,
			Requests:         counters.requests,
			Served:           counters.served,
			Failed:           counters.failed,
			PromptTokens:     counters.promptTokens,
			CompletionTokens: counters.completionTokens,
			P50MS:            percentile(c.latencies[name], 50),
			P95MS:            percentile(c.latencies[name], 95),
		})
	}

	models := make([]ModelStat, 0, len(c.perModel))
	for name, counters := range c.perModel {
		avg := int64(0)
		if counters.requests > 0 {
			avg = counters.latencySumMS / int64(counters.requests)
		}
		models = append(models, ModelStat{
			Name:             name,
			Requests:         counters.requests,
			OK:               counters.ok,
			PromptTokens:     counters.promptTokens,
			CompletionTokens: counters.completionTokens,
			AvgMS:            avg,
		})
	}
	sort.Slice(models, func(i, j int) bool { return models[i].Requests > models[j].Requests })
	if len(models) > maxSummaryModels {
		models = models[:maxSummaryModels]
	}

	return Summary{
		Enabled:               enabled,
		UptimeSeconds:         int64(time.Since(c.startedAt).Seconds()),
		TotalRequests:         c.totalRequests,
		TotalServed:           c.totalServed,
		TotalFailed:           c.totalFailed,
		TotalFallovers:        c.totalFallovers,
		TotalPromptTokens:     c.totalPromptTokens,
		TotalCompletionTokens: c.totalCompletionTokens,
		Channels:              channels,
		Models:                models,
	}
}

// Events returns the event feed, newest first.
func (c *Collector) Events(limit int) []Event {
	c.mu.RLock()
	defer c.mu.RUnlock()

	start := len(c.events) - limit
	if start < 0 {
		start = 0
	}
	reversed := make([]Event, 0, len(c.events)-start)
	for i := len(c.events) - 1; i >= start; i-- {
		reversed = append(reversed, c.events[i])
	}
	return reversed
}

func (c *Collector) channelLocked(name string) *channelCounters {
	counters, ok := c.perChannel[name]
	if !ok {
		counters = &channelCounters{}
		c.perChannel[name] = counters
	}
	return counters
}

func nowStamp() string {
	return time.Now().Format("15:04:05")
}

// Snapshot is the serializable form of the counters, written to disk so
// totals survive a restart. Events and traces are deliberately excluded:
// they are short-lived diagnostics, not accounting.
type Snapshot struct {
	TotalRequests         uint64 `json:"total_requests"`
	TotalServed           uint64 `json:"total_served"`
	TotalFailed           uint64 `json:"total_failed"`
	TotalFallovers        uint64 `json:"total_fallovers"`
	TotalPromptTokens     uint64 `json:"total_prompt_tokens"`
	TotalCompletionTokens uint64 `json:"total_completion_tokens"`
	// PerChannel maps channel name to [requests, served, failed, prompt, completion].
	PerChannel map[string][5]uint64 `json:"per_channel,omitempty"`
	// PerModel maps model name to [requests, ok, prompt, completion].
	// Latency sum is not persisted: the avg of old + new traffic would lie.
	PerModel map[string][4]uint64 `json:"per_model,omitempty"`
}

// Export copies the current counters into a serializable snapshot.
func (c *Collector) Export() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	snapshot := Snapshot{
		TotalRequests:         c.totalRequests,
		TotalServed:           c.totalServed,
		TotalFailed:           c.totalFailed,
		TotalFallovers:        c.totalFallovers,
		TotalPromptTokens:     c.totalPromptTokens,
		TotalCompletionTokens: c.totalCompletionTokens,
		PerChannel:            make(map[string][5]uint64, len(c.perChannel)),
		PerModel:              make(map[string][4]uint64, len(c.perModel)),
	}
	for name, counters := range c.perChannel {
		snapshot.PerChannel[name] = [5]uint64{
			counters.requests, counters.served, counters.failed,
			counters.promptTokens, counters.completionTokens,
		}
	}
	for name, counters := range c.perModel {
		snapshot.PerModel[name] = [4]uint64{
			counters.requests, counters.ok,
			counters.promptTokens, counters.completionTokens,
		}
	}
	return snapshot
}

// Restore merges a previously saved snapshot into the (presumably fresh)
// collector. Unknown channels are kept: a renamed channel coming back picks
// its history up again.
func (c *Collector) Restore(snapshot Snapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totalRequests += snapshot.TotalRequests
	c.totalServed += snapshot.TotalServed
	c.totalFailed += snapshot.TotalFailed
	c.totalFallovers += snapshot.TotalFallovers
	c.totalPromptTokens += snapshot.TotalPromptTokens
	c.totalCompletionTokens += snapshot.TotalCompletionTokens
	for name, values := range snapshot.PerChannel {
		counters := c.channelLocked(name)
		counters.requests += values[0]
		counters.served += values[1]
		counters.failed += values[2]
		counters.promptTokens += values[3]
		counters.completionTokens += values[4]
	}
	for name, values := range snapshot.PerModel {
		counters, ok := c.perModel[name]
		if !ok {
			counters = &modelCounters{}
			c.perModel[name] = counters
		}
		counters.requests += values[0]
		counters.ok += values[1]
		counters.promptTokens += values[2]
		counters.completionTokens += values[3]
	}
}

// Save writes the snapshot atomically (temp file + rename).
func Save(path string, snapshot Snapshot) error {
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

// Load reads a saved snapshot. A missing or corrupt file is an error the
// caller may ignore (stats are observational).
func Load(path string) (Snapshot, error) {
	var snapshot Snapshot
	raw, err := os.ReadFile(path)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
