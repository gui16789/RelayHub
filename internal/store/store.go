package store

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/local/relayhub/internal/config"
)

// Store owns the live, mutable copy of the config. Relay handlers read
// snapshots from it on every request, the admin API mutates it and persists
// every change straight back to the yaml file. The file is also re-read on
// demand when it changed on disk, so hand edits show up without a restart.
type Store struct {
	mu         sync.RWMutex
	path       string
	cfg        *config.Config
	lastLoaded *time.Time
	// lastCheck throttles the on-disk mtime probe so a busy proxy does not
	// os.Stat the config file on every single request.
	lastCheck time.Time
}

// reloadCheckInterval is how often Snapshot/IsEnabled look at the config
// file's mtime. Hand edits show up within this window at worst.
const reloadCheckInterval = 500 * time.Millisecond

func NewStore(path string) (*Store, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	store := &Store{path: path, cfg: cfg}
	if info, err := os.Stat(path); err == nil {
		stamp := info.ModTime()
		store.lastLoaded = &stamp
	}
	return store, nil
}

// Snapshot returns a deep copy so handlers never observe mid-mutation state.
// It reloads the file if it changed on disk (checked at most once per
// reloadCheckInterval), so edits made outside the console (e.g. by hand)
// are picked up automatically without a stat syscall on every request.
func (s *Store) Snapshot() *config.Config {
	s.maybeReload()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneConfig(s.cfg)
}

// Path returns the config file this store is bound to, for display.
func (s *Store) Path() string { return s.path }

// IsEnabled is the master switch read by the relay on every request.
func (s *Store) IsEnabled() bool {
	s.maybeReload()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg.Server.IsEnabled()
}

// maybeReload re-reads the config file when its mtime moved, but probes the
// filesystem at most once per reloadCheckInterval.
func (s *Store) maybeReload() {
	s.mu.RLock()
	fresh := time.Since(s.lastCheck) < reloadCheckInterval
	s.mu.RUnlock()
	if fresh {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastCheck = time.Now()
	s.reloadIfChangedLocked()
}

// reloadIfChangedLocked re-reads the config file when its modification time
// moved since we last wrote or loaded it. A parse failure keeps the in-memory
// config so a bad hand edit never breaks a running proxy.
func (s *Store) reloadIfChangedLocked() {
	current, err := os.Stat(s.path)
	if err != nil {
		return
	}
	if s.lastLoaded != nil && !current.ModTime().After(*s.lastLoaded) {
		return
	}
	reloaded, err := config.Load(s.path)
	if err != nil {
		return
	}
	s.cfg = reloaded
	stamp := current.ModTime()
	s.lastLoaded = &stamp
}

// SetProxyEnabled flips the master switch and persists it.
func (s *Store) SetProxyEnabled(enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Server.Enabled = &enabled
	return s.saveLocked()
}

// SetServerAPIKey sets (or clears, when empty) the key third-party clients
// must present as Bearer token, and persists it.
func (s *Store) SetServerAPIKey(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg.Server.APIKey = strings.TrimSpace(key)
	return s.saveLocked()
}

// ListChannels returns a copy of all channels in config order.
func (s *Store) ListChannels() []config.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	channels := make([]config.Channel, len(s.cfg.Channels))
	for i, channel := range s.cfg.Channels {
		channels[i] = cloneChannel(channel)
	}
	return channels
}

// AddChannel validates then appends a new channel. Names must be unique.
func (s *Store) AddChannel(channel config.Channel) error {
	if err := config.ValidateChannel(&channel); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.indexOfLocked(channel.Name) >= 0 {
		return fmt.Errorf("channel %q already exists", channel.Name)
	}
	s.cfg.Channels = append(s.cfg.Channels, cloneChannel(channel))
	return s.saveLocked()
}

// UpdateChannel replaces the channel with the same name, if it exists.
// An empty api_keys list means "keep the existing keys". The enabled flag
// is NOT part of the form, so the stored value is preserved.
func (s *Store) UpdateChannel(originalName string, channel config.Channel) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := s.indexOfLocked(originalName)
	if index < 0 {
		return fmt.Errorf("channel %q not found", originalName)
	}
	if len(channel.APIKeys) == 0 {
		existing := s.cfg.Channels[index].APIKeys
		channel.APIKeys = make([]string, len(existing))
		copy(channel.APIKeys, existing)
	}
	channel.Enabled = s.cfg.Channels[index].Enabled
	if err := config.ValidateChannel(&channel); err != nil {
		return err
	}
	if channel.Name != originalName && s.indexOfLocked(channel.Name) >= 0 {
		return fmt.Errorf("channel %q already exists", channel.Name)
	}
	s.cfg.Channels[index] = cloneChannel(channel)
	return s.saveLocked()
}

// SetChannelEnabled toggles a single channel without touching its other fields.
func (s *Store) SetChannelEnabled(name string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexOfLocked(name)
	if index < 0 {
		return fmt.Errorf("channel %q not found", name)
	}
	s.cfg.Channels[index].Enabled = &enabled
	return s.saveLocked()
}

// UpdateChannelModelMap replaces the channel's model_map without touching
// anything else (in particular the API keys, which the admin API only ever
// sees masked). A nil map clears the mapping.
func (s *Store) UpdateChannelModelMap(name string, modelMap map[string]string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index := s.indexOfLocked(name)
	if index < 0 {
		return fmt.Errorf("channel %q not found", name)
	}
	if modelMap != nil {
		cleaned := make(map[string]string, len(modelMap))
		for client, upstream := range modelMap {
			client, upstream = strings.TrimSpace(client), strings.TrimSpace(upstream)
			if client == "" || upstream == "" {
				return fmt.Errorf("模型名映射每行必须为 客户端名=上游名，两项都不能为空")
			}
			cleaned[client] = upstream
		}
		s.cfg.Channels[index].ModelMap = cleaned
	} else {
		s.cfg.Channels[index].ModelMap = nil
	}
	return s.saveLocked()
}

// DeleteChannel removes a channel by name.
func (s *Store) DeleteChannel(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.indexOfLocked(name)
	if index < 0 {
		return fmt.Errorf("channel %q not found", name)
	}
	s.cfg.Channels = append(s.cfg.Channels[:index], s.cfg.Channels[index+1:]...)
	return s.saveLocked()
}

// GetChannel returns the channel with the given name, INCLUDING unmasked
// api keys. Used by the local admin console to repopulate the edit form
// and by the model probe as a key source.
func (s *Store) GetChannel(name string) (config.Channel, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, channel := range s.cfg.Channels {
		if channel.Name == name {
			return cloneChannel(channel), nil
		}
	}
	return config.Channel{}, fmt.Errorf("channel %q not found", name)
}

func (s *Store) indexOfLocked(name string) int {
	for i, channel := range s.cfg.Channels {
		if channel.Name == name {
			return i
		}
	}
	return -1
}

func (s *Store) saveLocked() error {
	if err := config.Save(s.path, s.cfg); err != nil {
		return err
	}
	if info, err := os.Stat(s.path); err == nil {
		stamp := info.ModTime()
		s.lastLoaded = &stamp
	}
	// We just wrote the file ourselves: no need to re-stat it immediately.
	s.lastCheck = time.Now()
	return nil
}

// cloneChannel deep-copies the slice fields. make is used instead of
// append([]T(nil), ...) so that an EMPTY slice stays empty and marshals as
// JSON [] instead of null (null would crash the console's .map() calls).
func cloneChannel(channel config.Channel) config.Channel {
	copied := channel
	copied.APIKeys = make([]string, len(channel.APIKeys))
	copy(copied.APIKeys, channel.APIKeys)
	copied.Models = make([]string, len(channel.Models))
	copy(copied.Models, channel.Models)
	if channel.ModelMap != nil {
		copied.ModelMap = make(map[string]string, len(channel.ModelMap))
		for client, upstream := range channel.ModelMap {
			copied.ModelMap[client] = upstream
		}
	}
	if channel.Headers != nil {
		copied.Headers = make(map[string]string, len(channel.Headers))
		for name, value := range channel.Headers {
			copied.Headers[name] = value
		}
	}
	return copied
}

func cloneConfig(cfg *config.Config) *config.Config {
	copied := *cfg
	copied.Channels = make([]config.Channel, len(cfg.Channels))
	for i, channel := range cfg.Channels {
		copied.Channels[i] = cloneChannel(channel)
	}
	return &copied
}
