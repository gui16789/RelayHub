package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/local/relayhub/internal/logging"
	"github.com/local/relayhub/internal/server"
	"github.com/local/relayhub/internal/store"
	"github.com/local/relayhub/internal/version"
)

// Headless entry point: runs the proxy and serves the admin console
// in a browser. The desktop GUI version lives in the repo root.
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Println(version.String())
			return
		}
	}

	configPath := "config.yaml"
	if len(os.Args) > 1 {
		configPath = os.Args[1]
	}

	cfgStore, err := store.NewStore(configPath)
	if err != nil {
		slog.Error("load config failed", "err", err)
		os.Exit(1)
	}

	// Same handler as the desktop build; a distinct file name keeps the
	// two binaries from sharing one lumberjack handle.
	if err := logging.Setup(cfgStore.Snapshot().Logging, "proxy-headless"); err != nil {
		slog.Error("init logging failed", "err", err)
		os.Exit(1)
	}

	service := server.New(cfgStore)
	snapshot := cfgStore.Snapshot()
	slog.Info("proxy listening",
		"listen", snapshot.Server.Listen,
		"channels", len(snapshot.Channels),
		"console", "http://localhost"+listenDisplay(snapshot.Server.Listen)+"/admin/")
	if snapshot.Server.AdminKey == "" {
		slog.Warn("admin_key not set: console is loopback-only; remote users can finish first-boot setup",
			"setup", "http://<server>"+listenDisplay(snapshot.Server.Listen)+"/admin/setup")
	}

	httpServer := &http.Server{Addr: snapshot.Server.Listen, Handler: service}
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server exited", "err", err)
			os.Exit(1)
		}
	}()

	// Graceful shutdown: drain in-flight requests, stop background jobs and
	// persist the final stats snapshot before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdownCtx)
	service.Close()
}

// listenDisplay turns "0.0.0.0:8787" into ":8787" purely for the startup log line.
func listenDisplay(listen string) string {
	if index := strings.LastIndex(listen, ":"); index >= 0 {
		return listen[index:]
	}
	return listen
}
