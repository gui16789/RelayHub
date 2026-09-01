package relay

import (
	"sync"
	"time"

	"github.com/local/relayhub/internal/config"
)

// HealthProbeFunc checks whether one channel's upstream answers its
// model-list endpoint. Injected by the wiring layer (server.New) because
// the probe implementations live in the admin package, which imports this
// one.
type HealthProbeFunc func(channelType, baseURL string, apiKeys []string) bool

// defaultProbeInterval is how often the background health loop re-checks
// every enabled channel.
const defaultProbeInterval = 60 * time.Second

// probeOnce checks every enabled channel once, records the result in the
// state, and pushes an event when a channel flips between up and down.
// Channels that disappeared from the config are pruned.
func (h *Handler) probeOnce() {
	h.probeMu.Lock()
	probe := h.healthProbe
	h.probeMu.Unlock()

	snapshot := h.source.Snapshot()
	live := make(map[string]bool, len(snapshot.Channels))
	var probables []config.Channel
	for _, channel := range snapshot.Channels {
		if !channel.IsEnabled() {
			continue
		}
		live[channel.Name] = true
		if probe == nil || channel.BaseURL == "" || len(channel.APIKeys) == 0 {
			continue
		}
		probables = append(probables, channel)
	}

	// Probe channels concurrently (bounded) so one slow upstream cannot
	// delay the health picture of every other channel.
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, maxConcurrentProbes)
	for _, channel := range probables {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(channel config.Channel) {
			defer wg.Done()
			defer func() { <-semaphore }()
			h.probeChannel(probe, channel)
		}(channel)
	}
	wg.Wait()
	h.state.PruneHealth(live)
}

// maxConcurrentProbes bounds how many upstream health checks run at once.
const maxConcurrentProbes = 4

func (h *Handler) probeChannel(probe HealthProbeFunc, channel config.Channel) {
	succeeded := probe(channel.Type, channel.BaseURL, channel.APIKeys)
	previous, _, _, had := h.state.HealthInfo(channel.Name)
	h.state.SetHealth(channel.Name, succeeded)
	current, _, _, _ := h.state.HealthInfo(channel.Name)
	switch {
	case !had && current == "down":
		h.stats.PushEvent("warn", channel.Name, "health: upstream unreachable, channel deprioritized")
	case previous == "up" && current == "down":
		h.stats.PushEvent("warn", channel.Name, "health: upstream unreachable, channel deprioritized")
	case previous == "down" && current == "up":
		h.stats.PushEvent("info", channel.Name, "health: upstream reachable again")
	}
}

// StartHealthProbing launches the background loop (idempotent). It runs
// one pass immediately so the console shows probe results without waiting
// for the first interval. With no probe function set (tests) it is a no-op.
func (h *Handler) StartHealthProbing(interval time.Duration) {
	if interval <= 0 {
		interval = defaultProbeInterval
	}
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	if h.probeStop != nil || h.healthProbe == nil {
		return
	}
	stop := make(chan struct{})
	h.probeStop = stop
	go func() {
		for {
			h.probeOnce()
			select {
			case <-stop:
				return
			case <-time.After(interval):
			}
		}
	}()
}

// StopHealthProbing terminates the background loop, if any.
func (h *Handler) StopHealthProbing() {
	h.probeMu.Lock()
	stop := h.probeStop
	h.probeStop = nil
	h.probeMu.Unlock()
	if stop != nil {
		close(stop)
	}
}

// SetHealthProbe installs the upstream checker. It must be called before
// StartHealthProbing.
func (h *Handler) SetHealthProbe(f HealthProbeFunc) {
	h.probeMu.Lock()
	defer h.probeMu.Unlock()
	h.healthProbe = f
}
