package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/sys/windows"

	"github.com/local/relayhub/internal/logging"
	"github.com/local/relayhub/internal/server"
	"github.com/local/relayhub/internal/store"
	"github.com/local/relayhub/internal/version"
)

//go:embed frontend/dist/index.html
var splashAssets embed.FS

const defaultConfigYAML = `server:
  listen: ":8787"
  api_key: ""
  enabled: true
channels: []
`

// Desktop entry point: opens a native window showing the admin console
// while the proxy HTTP server runs in-process. A headless variant lives
// in cmd/headless.
func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v":
			fmt.Println(version.String())
			return
		}
	}

	configPath := resolveConfigPath()
	if err := ensureConfigFile(configPath); err != nil {
		fatalDialog("无法创建配置文件", err.Error())
	}

	cfgStore, err := store.NewStore(configPath)
	if err != nil {
		fatalDialog("加载配置失败", err.Error())
	}

	// Route slog to stderr AND a rotated file under %APPDATA%: the GUI has
	// no terminal, so without this the desktop build's logs vanish.
	if err := logging.Setup(cfgStore.Snapshot().Logging, "proxy"); err != nil {
		fatalDialog("初始化日志失败", err.Error())
	}

	// Bind the port explicitly before opening the window. A silent bind
	// failure would otherwise leave the window pointing at a *different*
	// (older) instance that owns the port, showing a stale console.
	snapshot := cfgStore.Snapshot()
	listen := snapshot.Server.Listen
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		fatalDialog("端口被占用", fmt.Sprintf(
			"端口 %s 已被占用：可能已有一个代理实例在运行。\n请关闭旧的 RelayHub 窗口后重试，或修改 config.yaml 的 server.listen 使用其他端口。", listen))
		return
	}
	// Boot the HTTP server before the window so the proxy keeps serving
	// no matter what happens to the GUI lifecycle.
	service := server.New(cfgStore)
	go func() {
		if err := (&http.Server{Handler: service}).Serve(listener); err != nil {
			slog.Error("server failed", "listen", listen, "err", err)
		}
	}()

	app := &App{store: cfgStore}
	if err := wails.Run(&options.App{
		Title:     "RelayHub 管理控制台",
		Width:     1200,
		Height:    800,
		MinWidth:  900,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets: splashAssets,
		},
		BackgroundColour: &options.RGBA{R: 15, G: 17, B: 23, A: 255},
		OnStartup:        app.startup,
	}); err != nil {
		fatalDialog("桌面窗口启动失败", err.Error())
	}
	// Window closed: stop background jobs and persist the final stats.
	service.Close()
}

type App struct {
	store *store.Store
}

// startup runs once the window exists: wait for the already-launched HTTP
// server to accept connections, then point the webview at the console.
func (a *App) startup(ctx context.Context) {
	listen := a.store.Snapshot().Server.Listen
	port := portDisplay(listen)
	consoleURL := "http://localhost" + port + "/admin/"
	if !waitForPort(port, 5*time.Second) {
		runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
			Type:    runtime.WarningDialog,
			Title:   "启动超时",
			Message: fmt.Sprintf("代理服务在 5 秒内未在 %s 就绪（端口可能被占用），请修改 config.yaml 的 server.listen", listen),
		})
		return
	}
	slog.Info("proxy listening, opening console", "listen", listen, "console", consoleURL)
	// The webview starts on the embedded splash page; navigate it to the
	// console served by our own HTTP server.
	runtime.WindowExecJS(ctx, "window.location.href = '"+consoleURL+"';")
}

// waitForPort polls until the local port accepts TCP connections.
func waitForPort(port string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1"+port, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// portDisplay turns ":8787" or "0.0.0.0:8787" into ":8787".
func portDisplay(listen string) string {
	if index := strings.LastIndex(listen, ":"); index >= 0 {
		return listen[index:]
	}
	return listen
}

// resolveConfigPath prefers an explicit CLI argument, otherwise looks next
// to the executable so double-clicking the exe does not depend on CWD.
func resolveConfigPath() string {
	if len(os.Args) > 1 {
		return os.Args[1]
	}
	executablePath, err := os.Executable()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(filepath.Dir(executablePath), "config.yaml")
}

func ensureConfigFile(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(defaultConfigYAML), 0o600)
}

func fatalDialog(title, message string) {
	log.Printf("%s: %s", title, message)
	_, _ = windows.MessageBox(
		0,
		windows.StringToUTF16Ptr(message),
		windows.StringToUTF16Ptr(title),
		windows.MB_OK|windows.MB_ICONERROR,
	)
	os.Exit(1)
}
