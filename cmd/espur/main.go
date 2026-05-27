// Espur entrypoint. Boot sequence follows specs/bootstrap.dog.md;
// termination follows specs/shutdown.dog.md.
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
	"github.com/punny/espur/internal/adapter/wechat"
	"github.com/punny/espur/internal/bot"
	"github.com/punny/espur/internal/obs"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
	"github.com/punny/espur/internal/web"
)

// Exit codes per specs/shutdown.dog.md "Phase 3 — close resources."
const (
	exitOK            = 0
	exitDrainExceeded = 1
	exitResourceClose = 2
	exitBootFailed    = 3 // distinct from shutdown failure modes
)

func main() {
	code, err := run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "espur: %v\n", err)
	}
	os.Exit(code)
}

func run() (int, error) {
	// 1. Parse env. Required: ESPUR_MASTER_KEY.
	masterKey := strings.TrimSpace(os.Getenv("ESPUR_MASTER_KEY"))
	if masterKey == "" {
		return exitBootFailed, errors.New("ESPUR_MASTER_KEY is required")
	}
	dataDir := envOr("ESPUR_DATA_DIR", "./data")
	// XDG_DATA_HOME governs where opencode reads/writes auth.json. We default
	// it to a stable subdir of ESPUR_DATA_DIR so `opencode auth login` (run
	// once via `docker exec`) persists tokens that espur's children pick up
	// on subsequent invocations. See specs/oauth.dog.md.
	if os.Getenv("XDG_DATA_HOME") == "" {
		_ = os.Setenv("XDG_DATA_HOME", filepath.Join(dataDir, "xdg-data"))
	}
	webPort := envOr("ESPUR_WEB_PORT", "8080")
	logLevel := envOr("ESPUR_LOG_LEVEL", "info")
	dashboardURL := os.Getenv("ESPUR_DASHBOARD_URL")
	invokeTimeout := parseDuration(os.Getenv("ESPUR_OPENCODE_TIMEOUT"), 120*time.Second)
	drainDeadline := parseDuration(os.Getenv("ESPUR_SHUTDOWN_DRAIN"), 30*time.Second)
	if drainDeadline < invokeTimeout {
		// Spec: drain deadline ≥ invoke timeout, so the in-flight invocation
		// always has a chance to finish under its own clock.
		drainDeadline = invokeTimeout
	}

	logger := newLogger(logLevel)
	slog.SetDefault(logger)

	// 2. Open / create data dir + the opencode XDG home under it.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return exitBootFailed, fmt.Errorf("data dir: %w", err)
	}
	if err := os.MkdirAll(os.Getenv("XDG_DATA_HOME"), 0o755); err != nil {
		return exitBootFailed, fmt.Errorf("xdg data home: %w", err)
	}

	// 3. Open SQLite + migrations.
	db, err := store.Open(filepath.Join(dataDir, "espur.db"))
	if err != nil {
		return exitBootFailed, fmt.Errorf("open db: %w", err)
	}
	// db.Close handled below in shutdown phase 3 (not deferred — we want to
	// log the checkpoint outcome before close).

	// 4. Secrets self-test.
	vault, err := secrets.New(masterKey)
	if err != nil {
		_ = db.Close()
		return exitBootFailed, err
	}
	blob, _ := db.AnyCredentialBlob(context.Background())
	if err := vault.SelfTest(blob); err != nil {
		_ = db.Close()
		return exitBootFailed, fmt.Errorf("secrets self-test: %w", err)
	}
	logger.Info("secrets self-test passed", "event", obs.SecretsSelfTest)

	// 5. Vendor pool — state lives in DB; pool is a thin lookup over it.
	pool := vendor.New(db, vault).WithLogger(logger)

	// 6. Construct adapters (Discord only in v0.1; WeChat would join here).
	ts := transcript.NewStore(dataDir)

	core := bot.New(bot.Config{
		DB: db, Pool: pool, Transcript: ts,
		DashboardURL: dashboardURL, InvokeTimeout: invokeTimeout,
		Logger: logger,
	})

	// 7. Wire signal handling for graceful shutdown. We do NOT use
	// signal.NotifyContext here because spec/shutdown.dog.md requires a
	// distinct response to a second termination signal — we have to keep
	// listening past the first SIGTERM.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// adapterCtx: cancelled at phase-1 start. Adapter Start() loops watch
	// this. Web server also watches this — it stops accepting new HTTP
	// connections as soon as shutdown begins.
	adapterCtx, cancelAdapter := context.WithCancel(context.Background())

	// 8. Adapters.
	var adapters []adapter.Adapter
	if token := strings.TrimSpace(os.Getenv("ESPUR_DISCORD_TOKEN")); token != "" {
		d, err := discord.New(token)
		if err != nil {
			logger.Error("discord adapter construction failed", "event", obs.AdapterDisconnected, "err", err.Error())
		} else {
			core.RegisterAdapter(d)
			ch, err := d.Start(adapterCtx)
			if err != nil {
				logger.Error("discord start failed", "event", obs.AdapterDisconnected, "err", err.Error())
			} else {
				adapters = append(adapters, d)
				go func() {
					for ev := range ch {
						// Dispatch uses adapterCtx — it's only used to walk
						// off the dispatch path quickly. HandleTrigger runs
						// under a detached exec context inside the bot core.
						core.Dispatch(adapterCtx, ev)
					}
				}()
			}
		}
	} else {
		logger.Warn("ESPUR_DISCORD_TOKEN unset — Discord adapter not started")
	}

	// WeChat (personal account via openwechat). Opt-in: spec/adapter.dog.md
	// notes Tencent's automation policy and the QR-login UX; only start when
	// the operator explicitly asks for it.
	if strings.EqualFold(os.Getenv("ESPUR_WECHAT_ENABLED"), "1") ||
		strings.EqualFold(os.Getenv("ESPUR_WECHAT_ENABLED"), "true") {
		storagePath := filepath.Join(dataDir, "wechat-session.json")
		wa, err := wechat.New(storagePath)
		if err != nil {
			logger.Error("wechat adapter construction failed",
				"event", obs.AdapterDisconnected, "err", err.Error())
		} else {
			wa.SetUUIDCallback(func(uuid string) {
				logger.Info("wechat login QR ready",
					"event", "wechat.login.uuid",
					"qr_url", "https://login.weixin.qq.com/qrcode/"+uuid)
			})
			core.RegisterAdapter(wa)
			ch, err := wa.Start(adapterCtx)
			if err != nil {
				logger.Error("wechat start failed",
					"event", obs.AdapterDisconnected, "err", err.Error())
			} else {
				adapters = append(adapters, wa)
				go func() {
					for ev := range ch {
						core.Dispatch(adapterCtx, ev)
					}
				}()
			}
		}
	}

	// 9. Web UI on its own goroutine; we sequence its shutdown ourselves.
	server := web.New(db, vault, pool, ts)
	for _, a := range adapters {
		server.RegisterAdapter(a)
	}
	webErrCh := make(chan error, 1)
	go func() { webErrCh <- server.ListenAndServe(adapterCtx, ":"+webPort) }()

	logger.Info("boot complete",
		"event", obs.BootReady,
		"web_addr", ":"+webPort,
		"data_dir", dataDir,
		"invoke_timeout", invokeTimeout.String(),
		"drain_deadline", drainDeadline.String(),
		"adapters", len(adapters),
	)

	// 10. Wait for first signal OR web crash.
	var firstSig os.Signal
	select {
	case s := <-sigCh:
		firstSig = s
	case err := <-webErrCh:
		if err != nil {
			logger.Error("web server exited unexpectedly",
				"event", obs.ShutdownWeb, "err", err.Error())
		}
		// Treat as a self-initiated shutdown.
		firstSig = syscall.SIGTERM
	}

	exitCode := exitOK
	logger.Info("shutdown begin",
		"event", obs.ShutdownStart,
		"signal", firstSig.String(),
		"drain_deadline", drainDeadline.String())

	// Phase 1 — stop accepting new work.
	core.StopAccepting()
	cancelAdapter() // close adapter inbound + web new-conn acceptance

	// Phase 2 — drain in-flight invocations with deadline.
	drainCtx, cancelDrain := context.WithTimeout(context.Background(), drainDeadline)
	defer cancelDrain()

	drainDone := make(chan bool, 1)
	go func() { drainDone <- core.WaitDrain(drainCtx) }()

	select {
	case ok := <-drainDone:
		if ok {
			logger.Info("bot drain complete", "event", obs.ShutdownBot)
		} else {
			logger.Warn("drain deadline exceeded; aborting in-flight",
				"event", obs.ShutdownBot, "drain_deadline", drainDeadline.String())
			core.AbortInFlight()
			exitCode = exitDrainExceeded
			<-drainDone // wait for the goroutine to actually return
		}
	case s := <-sigCh:
		// Second signal: escalate per spec.
		logger.Warn("second signal — escalating shutdown",
			"event", obs.ShutdownStart, "signal", s.String())
		core.AbortInFlight()
		exitCode = exitDrainExceeded
		<-drainDone
	}

	// Phase 3 — close resources.
	if err := <-webErrCh; err != nil && !errors.Is(err, context.Canceled) {
		logger.Warn("web shutdown returned error", "event", obs.ShutdownWeb, "err", err.Error())
	} else {
		logger.Info("web shutdown complete", "event", obs.ShutdownWeb)
	}
	logger.Info("adapters shutdown complete", "event", obs.ShutdownAdapter, "count", len(adapters))

	if err := db.Checkpoint(context.Background()); err != nil {
		logger.Warn("wal checkpoint failed (db still consistent)",
			"event", obs.ShutdownStore, "err", err.Error())
	}
	if err := db.Close(); err != nil {
		logger.Error("db close failed", "event", obs.ShutdownStore, "err", err.Error())
		if exitCode == exitOK {
			exitCode = exitResourceClose
		}
	} else {
		logger.Info("db close complete", "event", obs.ShutdownStore)
	}

	logger.Info("shutdown complete",
		"event", obs.ShutdownComp, "exit_code", exitCode)
	return exitCode, nil
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
	// JSON to stdout per specs/observability.dog.md ("all logs are written
	// to stdout as structured records, one JSON object per line").
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
