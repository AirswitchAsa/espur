// Espur entrypoint. Boot sequence follows specs/bootstrap.dog.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/adapter/discord"
	"github.com/punny/espur/internal/bot"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
	"github.com/punny/espur/internal/web"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "espur: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// 1. Parse env. Required: ESPUR_MASTER_KEY.
	masterKey := strings.TrimSpace(os.Getenv("ESPUR_MASTER_KEY"))
	if masterKey == "" {
		return errors.New("ESPUR_MASTER_KEY is required")
	}
	dataDir := envOr("ESPUR_DATA_DIR", "./data")
	webPort := envOr("ESPUR_WEB_PORT", "8080")
	logLevel := envOr("ESPUR_LOG_LEVEL", "info")
	dashboardURL := os.Getenv("ESPUR_DASHBOARD_URL")
	invokeTimeout := parseDuration(os.Getenv("ESPUR_OPENCODE_TIMEOUT"), 120*time.Second)

	logger := newLogger(logLevel)
	slog.SetDefault(logger)

	// 2. Open / create data dir.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}

	// 3. Open SQLite + migrations.
	db, err := store.Open(filepath.Join(dataDir, "espur.db"))
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer db.Close()

	// 4. Secrets self-test.
	vault, err := secrets.New(masterKey)
	if err != nil {
		return err
	}
	blob, _ := db.AnyCredentialBlob(context.Background())
	if err := vault.SelfTest(blob); err != nil {
		return fmt.Errorf("secrets self-test: %w", err)
	}

	// 5. Vendor pool — state lives in DB; pool is a thin lookup over it.
	pool := vendor.New(db, vault)

	// 6. Construct adapters (Discord only in v0.1; WeChat would join here).
	ts := transcript.NewStore(dataDir)

	core := bot.New(bot.Config{
		DB: db, Pool: pool, Transcript: ts,
		DashboardURL: dashboardURL, InvokeTimeout: invokeTimeout,
		Logger: logger,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if token := strings.TrimSpace(os.Getenv("ESPUR_DISCORD_TOKEN")); token != "" {
		d, err := discord.New(token)
		if err != nil {
			logger.Error("discord adapter construction failed", "err", err)
		} else {
			core.RegisterAdapter(d)
			ch, err := d.Start(ctx)
			if err != nil {
				logger.Error("discord start failed", "err", err)
			} else {
				go func() {
					for ev := range ch {
						core.Dispatch(ctx, ev)
					}
				}()
			}
		}
	} else {
		logger.Warn("ESPUR_DISCORD_TOKEN unset — Discord adapter not started")
	}

	// 7. Web UI.
	server := web.New(db, vault, pool, ts)
	logger.Info("event=boot.ready", "web_addr", ":"+webPort)
	if err := server.ListenAndServe(ctx, ":"+webPort); err != nil {
		return fmt.Errorf("web: %w", err)
	}
	return nil
}

// ---- helpers ----

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseDuration(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return def
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

// _ ensures adapter import remains referenced for future adapters wired here.
var _ = adapter.Event{}
