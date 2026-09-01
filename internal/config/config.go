package config

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Supported channel protocol types.
const (
	TypeOpenAI    = "openai"
	TypeAnthropic = "anthropic"
	TypeGemini    = "gemini"
)

// Key strategies for channels with multiple API keys (global, server-level).
const (
	// KeyStrategyRoundRobin: every request advances the starting key, so
	// load is spread evenly across all keys (the default behavior).
	KeyStrategyRoundRobin = "round_robin"
	// KeyStrategyPreferredFirst: always try the first configured key first
	// and only fail over to the next key when the preferred one errors or is
	// cooling down. Keeps the upstream account stable so its prompt/context
	// cache stays warm.
	KeyStrategyPreferredFirst = "preferred_first"
)

type Server struct {
	Listen string `yaml:"listen" json:"listen"`
	// Optional: clients must present this as Bearer token. Empty means no auth.
	APIKey string `yaml:"api_key" json:"api_key"`
	// AdminKey guards the /admin/ console for non-loopback clients. Empty
	// means the console is only reachable from 127.0.0.1/::1.
	AdminKey string `yaml:"admin_key,omitempty" json:"admin_key,omitempty"`
	// MaxAttempts caps how many channel/key combinations a single request
	// tries before giving up. 0 (or absent) means unlimited.
	MaxAttempts int `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	// KeyStrategy is the GLOBAL key rotation strategy applied to every
	// channel with multiple keys. Empty or "round_robin" = rotate the
	// starting key every request (default); "preferred_first" = always try
	// the first key first, fail over only on error / cooldown (better
	// upstream cache affinity).
	KeyStrategy string `yaml:"key_strategy,omitempty" json:"key_strategy,omitempty"`
	// nil means enabled; the admin console can flip it at runtime and persist here.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// IsEnabled treats a missing flag as "on" so old config files keep working.
func (s *Server) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// normalizeServer validates the server-level settings.
func (s *Server) normalizeServer() error {
	s.KeyStrategy = strings.ToLower(strings.TrimSpace(s.KeyStrategy))
	switch s.KeyStrategy {
	case "", KeyStrategyRoundRobin, KeyStrategyPreferredFirst:
	default:
		return fmt.Errorf("unsupported key_strategy %q (allowed: round_robin, preferred_first)", s.KeyStrategy)
	}
	return nil
}

// Logging configures where the process-wide slog output goes. All fields
// are optional; absent fields fall back to sensible defaults so an old
// config.yaml without a logging: section keeps working unchanged.
type Logging struct {
	// Level: debug | info | warn | error. Default "info".
	Level string `yaml:"level,omitempty" json:"level,omitempty"`
	// Dir holds the log files. Empty = the OS per-user config directory
	// (%APPDATA% on Windows) joined with "RelayHub/logs".
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`
	// File is the log file name inside Dir. Empty = "<appName>.log" so the
	// desktop and headless binaries do not fight over one file.
	File string `yaml:"file,omitempty" json:"file,omitempty"`
}

type Channel struct {
	Name    string   `yaml:"name" json:"name"`
	Type    string   `yaml:"type" json:"type"` // openai | anthropic | gemini
	BaseURL string   `yaml:"base_url" json:"base_url"`
	APIKeys []string `yaml:"api_keys" json:"api_keys"`
	Models  []string `yaml:"models" json:"models"` // exact names or trailing-wildcard patterns like "claude-*"
	// ModelMap translates client-facing model names into the upstream's own
	// names when forwarding (e.g. {"deepseek-chat": "deepseek-chat-v3"}).
	// Empty or absent means the model name passes through unchanged.
	ModelMap map[string]string `yaml:"model_map,omitempty" json:"model_map,omitempty"`
	// Headers adds (or overrides) request headers on the outgoing upstream
	// request, e.g. {"X-Title": "proxy"} for gateways that require a
	// title, or a different User-Agent. Authorization and Content-Type are
	// the proxy's own and cannot be overridden this way.
	Headers  map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	Priority int               `yaml:"priority" json:"priority"`
	Enabled  *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"` // nil means enabled
}

// UpstreamModel returns the model name this channel's upstream actually
// knows: the mapped name when ModelMap declares it, otherwise the client's
// name unchanged.
func (c *Channel) UpstreamModel(model string) string {
	if mapped, ok := c.ModelMap[model]; ok && mapped != "" {
		return mapped
	}
	return model
}

// IsEnabled treats a missing flag as "on" so old config files keep working.
func (c *Channel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

func (c *Channel) normalize() error {
	if c.Name == "" {
		return fmt.Errorf("channel name is required")
	}
	if c.Type == "" {
		c.Type = TypeOpenAI
	}
	c.BaseURL = strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	// The relays always append their own versioned path (/v1/..., /v1beta/...),
	// so a trailing /v1 in the configured base_url would produce /v1/v1/...
	if strings.HasSuffix(c.BaseURL, "/v1") {
		c.BaseURL = strings.TrimSuffix(c.BaseURL, "/v1")
	}
	switch c.Type {
	case TypeOpenAI, TypeAnthropic, TypeGemini:
	default:
		return fmt.Errorf("channel %q has unsupported type %q", c.Name, c.Type)
	}
	if c.BaseURL == "" {
		return fmt.Errorf("channel %q has no base_url", c.Name)
	}
	if len(c.APIKeys) == 0 {
		return fmt.Errorf("channel %q has no api_keys", c.Name)
	}
	// Trim header names/values; drop entries with an empty name.
	if c.Headers != nil {
		for key, value := range c.Headers {
			trimmed := strings.TrimSpace(key)
			if trimmed == "" {
				delete(c.Headers, key)
				continue
			}
			if trimmed != key {
				c.Headers[trimmed] = value
				delete(c.Headers, key)
			}
			c.Headers[trimmed] = strings.TrimSpace(value)
		}
	}
	// The proxy owns authentication and the content type: per-channel
	// overrides would break key rotation and stream parsing.
	for name := range c.Headers {
		lower := strings.ToLower(name)
		if lower == "authorization" || lower == "content-type" {
			return fmt.Errorf("channel %q: header %q is reserved and cannot be overridden per channel", c.Name, name)
		}
	}
	return nil
}

type Config struct {
	Server   Server    `yaml:"server"`
	Channels []Channel `yaml:"channels"`
	// Logging is read once at startup (it points at files and a handler,
	// neither of which is hot-swappable). It is kept on the config so the
	// admin console's save path round-trips the section instead of
	// silently dropping it from the yaml.
	Logging Logging `yaml:"logging,omitempty" json:"logging,omitempty"`
}

// Default returns the empty in-memory config used when no config file
// exists yet (first boot): default listen address, no keys, no channels.
// The web setup wizard turns this into a real file on first save.
func Default() *Config {
	return &Config{Server: Server{Listen: ":8787"}, Channels: []Channel{}}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Server.Listen == "" {
		cfg.Server.Listen = ":8787"
	}
	if err := cfg.Server.normalizeServer(); err != nil {
		return nil, err
	}
	expandEnv(cfg)
	for i := range cfg.Channels {
		if err := cfg.Channels[i].normalize(); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// expandEnv resolves ${VAR} / $VAR references in secret and endpoint fields
// so API keys can live in the environment instead of plain text on disk.
func expandEnv(cfg *Config) {
	cfg.Server.APIKey = os.ExpandEnv(cfg.Server.APIKey)
	cfg.Server.AdminKey = os.ExpandEnv(cfg.Server.AdminKey)
	for i := range cfg.Channels {
		channel := &cfg.Channels[i]
		channel.BaseURL = os.ExpandEnv(channel.BaseURL)
		for j, key := range channel.APIKeys {
			channel.APIKeys[j] = os.ExpandEnv(key)
		}
		for name, value := range channel.Headers {
			channel.Headers[name] = os.ExpandEnv(value)
		}
	}
}

// Save writes the config back to disk. A temp file + rename makes the write
// atomic enough that a crash mid-save cannot truncate the real config.
func Save(path string, cfg *Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, raw, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// ValidateChannel checks a channel definition the way Load would, without
// requiring it to be inside a full config.
func ValidateChannel(channel *Channel) error {
	return channel.normalize()
}

// SupportsModel reports whether the channel serves the requested model,
// supporting trailing-wildcard patterns such as "claude-*".
func (c *Channel) SupportsModel(model string) bool {
	for _, pattern := range c.Models {
		if matchModelPattern(pattern, model) {
			return true
		}
	}
	return false
}

func matchModelPattern(pattern, model string) bool {
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(model, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == model
}

// CandidateChannels returns enabled channels able to serve the model,
// highest priority first. Channels with equal priority keep config order.
func (cfg *Config) CandidateChannels(model string) []Channel {
	candidates := make([]Channel, 0, len(cfg.Channels))
	for _, channel := range cfg.Channels {
		if channel.IsEnabled() && channel.SupportsModel(model) {
			candidates = append(candidates, channel)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Priority > candidates[j].Priority
	})
	return candidates
}

// AllModels aggregates every concrete model declared by an ENABLED channel,
// deduplicated and sorted. Wildcard patterns are skipped. Keeping this
// consistent with CandidateChannels means /v1/models never lists a model
// that a live request cannot route to (a disabled channel's models vanish
// from the list until the channel is re-enabled).
func (cfg *Config) AllModels() []string {
	seen := make(map[string]bool)
	models := make([]string, 0)
	for _, channel := range cfg.Channels {
		if !channel.IsEnabled() {
			continue
		}
		for _, pattern := range channel.Models {
			if strings.HasSuffix(pattern, "*") || seen[pattern] {
				continue
			}
			seen[pattern] = true
			models = append(models, pattern)
		}
	}
	sort.Strings(models)
	return models
}
