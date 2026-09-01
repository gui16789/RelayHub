package server

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/local/relayhub/internal/admin"
	"github.com/local/relayhub/internal/relay"
	"github.com/local/relayhub/internal/stats"
	"github.com/local/relayhub/internal/store"
)

// Service is the running proxy: the HTTP handler plus the background jobs
// (health probing, stats persistence) that must stop on shutdown.
type Service struct {
	http.Handler
	handler   *relay.Handler
	collector *stats.Collector
	statsPath string
	stopStats chan struct{}
}

// New wires the relay, model listing and admin console into one handler,
// and starts the background health-probe loop that keeps the routing layer
// aware of unreachable upstreams between requests.
func New(cfgStore *store.Store) *Service {
	collector := stats.NewCollector()
	statsPath := filepath.Join(filepath.Dir(cfgStore.Path()), "stats.json")
	if restored, err := stats.Load(statsPath); err == nil {
		collector.Restore(restored)
	} else {
		slog.Debug("no prior stats restored", "path", statsPath, "err", err)
	}

	handler := relay.NewHandler(cfgStore, collector)
	// Quota cooldowns are hour-scale: restore them across restarts so a
	// reboot does not wake every exhausted key at once.
	handler.State().SetPersistence(filepath.Join(filepath.Dir(cfgStore.Path()), "cooldowns.json"))
	handler.SetHealthProbe(func(channelType, baseURL string, apiKeys []string) bool {
		_, err := admin.ListModels(channelType, baseURL, apiKeys)
		return err == nil
	})
	handler.StartHealthProbing(0) // default 60s interval
	adminServer := admin.NewServer(cfgStore, handler, collector)

	mux := http.NewServeMux()
	// Chat completions get full protocol conversion (anthropic/gemini);
	// every other OpenAI-style endpoint is proxied verbatim to channels
	// that natively speak the protocol (type openai).
	mux.Handle("/v1/chat/completions", handler)
	mux.Handle("/v1/responses", handler)
	mux.Handle("/v1/embeddings", handler)
	mux.Handle("/v1/images/", handler)
	mux.Handle("/v1/audio/", handler)
	mux.Handle("/v1/moderations", handler)
	mux.HandleFunc("/v1/models", handleListModels(cfgStore))
	mux.Handle("/admin/", adminServer.Mux())
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/" {
			http.Redirect(writer, request, "/admin/", http.StatusFound)
			return
		}
		http.NotFound(writer, request)
	})

	service := &Service{
		Handler:   authMiddleware(cfgStore, mux),
		handler:   handler,
		collector: collector,
		statsPath: statsPath,
		stopStats: make(chan struct{}),
	}
	go service.persistStatsLoop()
	return service
}

// Close stops background jobs and writes the final stats snapshot.
func (s *Service) Close() {
	s.handler.StopHealthProbing()
	close(s.stopStats)
	if err := stats.Save(s.statsPath, s.collector.Export()); err != nil {
		slog.Warn("stats save failed", "err", err)
	}
}

// persistStatsLoop snapshots counters to disk once a minute so a crash does
// not lose everything since startup.
func (s *Service) persistStatsLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopStats:
			return
		case <-ticker.C:
			if err := stats.Save(s.statsPath, s.collector.Export()); err != nil {
				slog.Warn("stats save failed", "err", err)
			}
		}
	}
}

func handleListModels(cfgStore *store.Store) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		snapshot := cfgStore.Snapshot()
		type modelEntry struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		}
		entries := []modelEntry{}
		for _, model := range snapshot.AllModels() {
			entries = append(entries, modelEntry{ID: model, Object: "model", OwnedBy: "proxy"})
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(map[string]any{"object": "list", "data": entries})
	}
}

// authMiddleware enforces the proxy's own Bearer key when configured.
//
// The admin console is exempt from the client API key but is guarded
// separately: it answers loopback clients freely, and remote clients only
// when they present server.admin_key as a Bearer token. With no admin_key
// configured the console is loopback-only, which keeps "listen on 0.0.0.0"
// from exposing channel management (and unmasked keys) to the network.
func authMiddleware(cfgStore *store.Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		snapshot := cfgStore.Snapshot()
		provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")

		if strings.HasPrefix(request.URL.Path, "/admin/") {
			if isLoopback(request.RemoteAddr) {
				next.ServeHTTP(writer, request)
				return
			}
			if snapshot.Server.AdminKey != "" && constantTimeEqual(provided, snapshot.Server.AdminKey) {
				next.ServeHTTP(writer, request)
				return
			}
			http.Error(writer, "admin console requires loopback access or server.admin_key", http.StatusForbidden)
			return
		}

		if snapshot.Server.APIKey == "" {
			next.ServeHTTP(writer, request)
			return
		}
		if !constantTimeEqual(provided, snapshot.Server.APIKey) {
			http.Error(writer, "invalid api key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

// constantTimeEqual compares two secrets without leaking length-independent
// timing (empty strings never match).
func constantTimeEqual(provided, expected string) bool {
	if provided == "" || expected == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

// isLoopback reports whether the remote address of the connection is on
// this machine (127.0.0.0/8 or ::1).
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
