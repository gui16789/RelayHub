// Package logging wires the process-wide slog logger: stderr plus a
// size-rotated file, with a Windows-friendly default directory so the
// desktop GUI (which has no terminal) still leaves a diagnosable trail.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/relayhub/internal/config"
	"gopkg.in/natefinch/lumberjack.v2"
)

// Setup installs the default slog handler mirroring to stderr and to a
// rotated log file. appName distinguishes the desktop ("proxy") and
// headless ("proxy-headless") binaries so the two never share a file
// handle (lumberjack does not coordinate across processes).
func Setup(cfg config.Logging, appName string) error {
	level := parseLevel(cfg.Level)

	dir := cfg.Dir
	if dir == "" {
		// Desktop apps must NOT log next to the exe or into the CWD:
		// both may be read-only (Program Files, a zip extract, a double
		// click from an arbitrary folder). The per-user config dir is
		// the Windows-idiomatic, always-writable location.
		base, err := os.UserConfigDir()
		if err != nil {
			base = "."
		}
		dir = filepath.Join(base, "RelayHub", "logs")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create log dir %s: %w", dir, err)
	}

	name := cfg.File
	if name == "" {
		name = appName + ".log"
	}

	// stderr AND the file: headless runs watch stderr, the desktop GUI
	// has no console so the file is its only output. MultiWriter keeps
	// both working without any branching on the build mode.
	writer := io.MultiWriter(os.Stderr, &lumberjack.Logger{
		Filename:   filepath.Join(dir, name),
		MaxSize:    10, // MiB per file before rotation
		MaxBackups: 3,
		MaxAge:     14, // days
		Compress:   true,
	})

	handler := slog.NewTextHandler(writer, &slog.HandlerOptions{Level: level})
	slog.SetDefault(slog.New(handler))
	return nil
}

func parseLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
