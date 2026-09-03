package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"math/rand"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/local/relayhub/internal/config"
)

// State tracks per-channel key rotation counters, temporary key cooldowns,
// server-error streaks (error-aware cooling) and probe-based channel health.
type State struct {
	mu sync.Mutex
	// disabled holds key identity -> time until which the key is skipped.
	disabled map[string]cooldownEntry
	// nextIndex gives each channel a round-robin cursor so requests spread across keys.
	nextIndex map[string]*atomic.Uint64
	// serverStreaks holds key identity -> consecutive 5xx count.
	serverStreaks map[string]streakEntry
	// health holds channel name -> probe result. "" means never probed.
	health map[string]healthEntry
	// persistPath, when non-empty, is where quota cooldowns are written so
	// a restart does not wake every exhausted key at once.
	persistPath string
}

// cooldownKind distinguishes a short-lived rate-limit rest from a
// long-lived quota-exhaustion park. Quota entries escalate through a
// backoff ladder and survive restarts via persistence.
type cooldownKind string

const (
	cooldownRate  cooldownKind = "rate"  // transient rate limit (seconds/minutes)
	cooldownQuota cooldownKind = "quota" // quota window exhausted (hours)
	cooldownOther cooldownKind = "other" // auth/5xx/panic cooldowns
)

type cooldownEntry struct {
	channelName string
	// keyTail is the last 4 characters of the API key, kept for admin display.
	// The key itself is only ever held as a fingerprint (see keyIdentity), so
	// this is the sole human-recognizable remnant.
	keyTail string
	until   time.Time
	kind    cooldownKind
	// attempts counts consecutive quota strikes; it drives the backoff
	// ladder and only resets after a successful request (half-open probe).
	attempts int
}

// streakEntry counts consecutive 5xx responses for one key; the streak
// expires if the channel was quiet for longer than streakWindow.
type streakEntry struct {
	count    int
	lastSeen time.Time
}

type healthEntry struct {
	status   string // "up" | "down"
	checked  time.Time
	failures int // consecutive probe failures
}

func NewState() *State {
	return &State{
		disabled:      make(map[string]cooldownEntry),
		nextIndex:     make(map[string]*atomic.Uint64),
		serverStreaks: make(map[string]streakEntry),
		health:        make(map[string]healthEntry),
	}
}

// CooldownInfo is the admin-facing view of a key currently in cooldown.
type CooldownInfo struct {
	Channel  string    `json:"channel"`
	KeyTail  string    `json:"key_tail"`
	Until    time.Time `json:"until"`
	RemainMS int64     `json:"remain_ms"`
	Kind     string    `json:"kind"`
	Attempts int       `json:"attempts,omitempty"`
}

// Cooldowns lists keys currently in cooldown, most urgent first.
func (s *State) Cooldowns() []CooldownInfo {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	infos := make([]CooldownInfo, 0, len(s.disabled))
	for identity, entry := range s.disabled {
		if !now.Before(entry.until) {
			delete(s.disabled, identity)
			continue
		}
		infos = append(infos, CooldownInfo{
			Channel:  entry.channelName,
			KeyTail:  entry.keyTail,
			Until:    entry.until,
			RemainMS: entry.until.Sub(now).Milliseconds(),
			Kind:     string(entry.kind),
			Attempts: entry.attempts,
		})
	}
	return infos
}

// splitIdentity recovers the channel name and key fingerprint from a map key.
func splitIdentity(identity string) (channelName, fingerprint string) {
	parts := strings.SplitN(identity, "\x00", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return identity, ""
}

// keyFingerprint is a one-way, truncated SHA-256 of an API key. It is what
// identifies a key everywhere in this package, which is what lets the quota
// cooldown file be written without ever putting a usable credential on disk.
// 64 bits is far beyond what a handful of keys per channel needs to avoid
// collisions, and stays short enough for the file to remain readable.
func keyFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:8])
}

func keyIdentity(channelName, apiKey string) string {
	return channelName + "\x00" + keyFingerprint(apiKey)
}

// OrderedKeys returns the channel's usable keys in the order the router should
// try them, skipping any key currently in cooldown. The starting key depends on
// the GLOBAL (server-level) key_strategy:
//   - round_robin (default): the starting key advances on every request so
//     load is spread evenly across the channel's keys;
//   - preferred_first: always start from the first configured key. Failover to
//     the next key happens in the handler's attempt loop when the preferred key
//     errors or is cooling down; once the preferred key recovers from cooldown
//     it is automatically preferred again.
func (s *State) OrderedKeys(channel config.Channel, keyStrategy string) []string {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var start int
	if keyStrategy == config.KeyStrategyPreferredFirst {
		// Sticky first key: do not advance the round-robin cursor.
		start = 0
	} else {
		counter, ok := s.nextIndex[channel.Name]
		if !ok {
			counter = &atomic.Uint64{}
			s.nextIndex[channel.Name] = counter
		}
		start = int(counter.Add(1)-1) % len(channel.APIKeys)
	}

	ordered := make([]string, 0, len(channel.APIKeys))
	for offset := 0; offset < len(channel.APIKeys); offset++ {
		apiKey := channel.APIKeys[(start+offset)%len(channel.APIKeys)]
		if entry, cooling := s.disabled[keyIdentity(channel.Name, apiKey)]; cooling && now.Before(entry.until) {
			continue
		}
		ordered = append(ordered, apiKey)
	}
	return ordered
}

// Penalize puts a key into cooldown for the given duration.
func (s *State) Penalize(channelName, apiKey string, duration time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.disabled[keyIdentity(channelName, apiKey)] = cooldownEntry{
		channelName: channelName,
		keyTail:     tail(apiKey, 4),
		until:       time.Now().Add(duration),
		kind:        cooldownRate,
	}
	s.persistLocked()
}

// Quota backoff ladder: consecutive quota strikes park the key longer and
// longer so an exhausted quota window is probed sparsely instead of
// hammered every minute. Capped at quotaMaxCooldown; ±20% jitter spreads
// the wake-up so all keys do not probe the upstream at the same instant.
// Both values come from config.BuiltinCooldowns() — a channel can override
// them per channel via its cooldowns settings.
var (
	quotaBaseCooldown = config.BuiltinCooldowns().QuotaBase.D()
	quotaMaxCooldown  = config.BuiltinCooldowns().QuotaMax.D()
)

// PenalizeQuota parks a quota-exhausted key. When the upstream told us
// when the window resets (resetHint > 0) that hint wins (capped at max);
// otherwise the key escalates through the backoff ladder from base, based
// on its strike count. The applied duration is returned for logging.
func (s *State) PenalizeQuota(channelName, apiKey string, resetHint time.Duration, base, max time.Duration) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	identity := keyIdentity(channelName, apiKey)
	entry := s.disabled[identity]
	entry.channelName = channelName
	entry.keyTail = tail(apiKey, 4)
	entry.kind = cooldownQuota
	entry.attempts++

	if base <= 0 || max <= 0 {
		base = quotaBaseCooldown
		max = quotaMaxCooldown
	}
	duration := resetHint
	if duration <= 0 {
		duration = base << min(entry.attempts-1, 6)
		if duration > max {
			duration = max
		}
	} else if duration > max {
		duration = max
	}
	// ±20% jitter.
	jitter := 1 + (rand.Float64()-0.5)*0.4
	duration = time.Duration(float64(duration) * jitter)

	entry.until = time.Now().Add(duration)
	s.disabled[identity] = entry
	s.persistLocked()
	return duration
}

// ResetQuota clears the quota strike count for a key after a successful
// request: the half-open probe confirmed the window has refilled.
func (s *State) ResetQuota(channelName, apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity := keyIdentity(channelName, apiKey)
	if entry, ok := s.disabled[identity]; ok && entry.kind == cooldownQuota {
		delete(s.disabled, identity)
		s.persistLocked()
	}
}

// streakWindow bounds how old a server-error streak may be before it is
// forgotten: a channel that was quiet for a while is considered recovered.
const streakWindow = 5 * time.Minute

// serverCooldown is how long a key rests after tripping the 5xx streak
// threshold. Shorter than an auth failure (the key itself is fine), long
// enough that a genuinely dead upstream stops absorbing every request.
const serverCooldown = 60 * time.Second

// serverStreakThreshold is the number of consecutive 5xx responses within
// streakWindow that trip the error-aware cooldown.
const serverStreakThreshold = 3

// MarkServerError records one 5xx response for the key. It returns the new
// streak length; the caller decides whether the threshold was crossed.
// Streaks only grow on consecutive errors: an intervening success resets
// them, and an idle period longer than streakWindow starts a fresh count.
func (s *State) MarkServerError(channelName, apiKey string) int {
	identity := keyIdentity(channelName, apiKey)
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.serverStreaks[identity]
	if !ok || now.Sub(entry.lastSeen) > streakWindow {
		entry = streakEntry{}
	}
	entry.count++
	entry.lastSeen = now
	s.serverStreaks[identity] = entry
	return entry.count
}

// ResetServerStreak clears the 5xx streak for the key after a success.
func (s *State) ResetServerStreak(channelName, apiKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.serverStreaks, keyIdentity(channelName, apiKey))
}

// HealthInfo reports the probe-based health of a channel; ok is false when
// the channel has never been probed (treated as healthy, not down).
func (s *State) HealthInfo(channelName string) (status string, checkedAt time.Time, failures int, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.health[channelName]
	return entry.status, entry.checked, entry.failures, ok
}

// SetHealth records one probe result for a channel. A channel flips to
// "down" after downAfterFailures consecutive failures and back to "up" on
// the first success, so a flaky probe does not hide a working channel.
func (s *State) SetHealth(channelName string, succeeded bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.health[channelName]
	if !ok {
		entry = healthEntry{status: "up"}
	}
	if succeeded {
		entry.status = "up"
		entry.failures = 0
	} else {
		entry.failures++
		if entry.failures >= downAfterFailures {
			entry.status = "down"
		}
	}
	entry.checked = time.Now()
	s.health[channelName] = entry
}

// downAfterFailures consecutive probe failures mark a channel "down".
const downAfterFailures = 2

// IsDown reports whether the probe loop currently considers the channel
// unusable. Channels that were never probed are NOT down.
func (s *State) IsDown(channelName string) bool {
	status, _, _, ok := s.HealthInfo(channelName)
	return ok && status == "down"
}

// PruneHealth drops health entries for channels that no longer exist, so a
// renamed or deleted channel cannot haunt the state forever.
func (s *State) PruneHealth(live map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for name := range s.health {
		if !live[name] {
			delete(s.health, name)
		}
	}
}

// persistedCooldown is the on-disk shape of a quota cooldown. Only quota
// entries are persisted: short rate-limit rests are over by the time a
// restarted process could act on them anyway.
//
// No usable credential is stored. KeyHash is the one-way fingerprint that
// identifies the key in memory, so a restored entry re-attaches to the right
// key without the file ever holding something an attacker could spend.
type persistedCooldown struct {
	Channel  string    `json:"channel"`
	KeyTail  string    `json:"key_tail"` // last 4 chars, for admin display only
	KeyHash  string    `json:"key_hash"`
	Until    time.Time `json:"until"`
	Attempts int       `json:"attempts"`

	// LegacyKey reads the plaintext `key` field written by versions before
	// fingerprinting. It exists only so an upgrade can migrate those entries
	// instead of dropping them, which would wake every exhausted key at once.
	// It is never written back; see persistLocked.
	LegacyKey string `json:"key,omitempty"`
}

type cooldownSnapshot struct {
	Quota []persistedCooldown `json:"quota"`
}

// SetPersistence binds a JSON file for quota cooldowns and loads any
// still-valid entries from a previous run. Called once at startup.
func (s *State) SetPersistence(path string) {
	s.mu.Lock()
	s.persistPath = path
	s.mu.Unlock()
	if err := s.loadPersisted(); err != nil {
		slog.Debug("no prior cooldowns restored", "path", path, "err", err)
	}
}

func (s *State) loadPersisted() error {
	raw, err := os.ReadFile(s.persistPath)
	if err != nil {
		return err
	}
	var snapshot cooldownSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return err
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	migrated := 0
	for _, entry := range snapshot.Quota {
		if !now.Before(entry.Until) {
			continue
		}
		fingerprint := entry.KeyHash
		keyTail := entry.KeyTail
		if fingerprint == "" {
			// Pre-fingerprint file: derive the identity from the plaintext key
			// this once, so the cooldown survives the upgrade.
			if entry.LegacyKey == "" {
				continue
			}
			fingerprint = keyFingerprint(entry.LegacyKey)
			if keyTail == "" {
				keyTail = tail(entry.LegacyKey, 4)
			}
			migrated++
		}
		s.disabled[entry.Channel+"\x00"+fingerprint] = cooldownEntry{
			channelName: entry.Channel,
			keyTail:     keyTail,
			until:       entry.Until,
			kind:        cooldownQuota,
			attempts:    entry.Attempts,
		}
	}
	if migrated > 0 {
		// Rewrite immediately so the plaintext keys leave the disk now rather
		// than whenever the next quota strike happens to trigger a write.
		slog.Info("migrated plaintext cooldown entries to fingerprints", "count", migrated)
		s.persistLocked()
	}
	return nil
}

// persistLocked writes quota cooldowns to disk. The file is tiny (one
// entry per exhausted key) and quota strikes are rare, so a synchronous
// write-through is cheaper than a debounce loop. Caller holds s.mu.
func (s *State) persistLocked() {
	if s.persistPath == "" {
		return
	}
	now := time.Now()
	snapshot := cooldownSnapshot{Quota: []persistedCooldown{}}
	for identity, entry := range s.disabled {
		if entry.kind != cooldownQuota || !now.Before(entry.until) {
			continue
		}
		channelName, fingerprint := splitIdentity(identity)
		snapshot.Quota = append(snapshot.Quota, persistedCooldown{
			Channel:  channelName,
			KeyTail:  entry.keyTail,
			KeyHash:  fingerprint,
			Until:    entry.until,
			Attempts: entry.attempts,
		})
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	// Write-then-rename so a crash mid-write cannot corrupt the file.
	tmp := s.persistPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		slog.Warn("cooldown persist failed", "err", err)
		return
	}
	if err := os.Rename(tmp, s.persistPath); err != nil {
		slog.Warn("cooldown persist rename failed", "err", err)
	}
}
