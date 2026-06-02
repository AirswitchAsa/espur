// Command wechat-login drives the iLink QR-code login flow on its own and
// persists the session to <ESPUR_DATA_DIR>/wechat-session.json, so that the
// main `espur` process (started with ESPUR_WECHAT_ENABLED=1) can resume the
// WeChat adapter without prompting for a QR scan.
//
// It boots none of the rest of espur (no web server, no database, no port), so
// it is safe to run alongside a live espur deployment. Usage:
//
//	ESPUR_DATA_DIR=./data go run ./cmd/wechat-login
//
// Scan the printed QR with the WeChat mobile app; the tool exits once the
// session is connected and saved (or on Ctrl-C).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/adapter/wechat"
)

func main() {
	dataDir := os.Getenv("ESPUR_DATA_DIR")
	if dataDir == "" {
		dataDir = "./data"
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "wechat-login: data dir: %v\n", err)
		os.Exit(1)
	}
	storagePath := filepath.Join(dataDir, "wechat-session.json")

	var opts []wechat.Option
	if bn := os.Getenv("ESPUR_WECHAT_BOT_NAME"); bn != "" {
		opts = append(opts, wechat.WithBotName(bn))
	}
	wa, err := wechat.New(storagePath, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wechat-login: %v\n", err)
		os.Exit(1)
	}
	wa.SetQRCallback(func(content string) { wechat.WriteLoginQR(os.Stderr, content) })

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	fmt.Fprintf(os.Stderr, "wechat-login: starting iLink login; session will be saved to %s\n", storagePath)
	ch, err := wa.Start(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wechat-login: start: %v\n", err)
		os.Exit(1)
	}

	exit := 0
	for ev := range ch {
		if ev.Lifecycle == nil {
			continue
		}
		switch ev.Lifecycle.Kind {
		case adapter.LifecycleConnected:
			fmt.Fprintf(os.Stderr, "wechat-login: connected — session saved to %s\n", storagePath)
			fmt.Fprintln(os.Stderr, "wechat-login: you can now start espur with ESPUR_WECHAT_ENABLED=1 (it will resume without a QR).")
			cancel() // stop the poll loop; we only needed the login
		case adapter.LifecycleAuthRevoked:
			fmt.Fprintf(os.Stderr, "wechat-login: login failed/expired: %s\n", ev.Lifecycle.Cause)
			exit = 1
			cancel()
		case adapter.LifecycleDisconnected:
			if ev.Lifecycle.Cause != "" {
				fmt.Fprintf(os.Stderr, "wechat-login: disconnected: %s\n", ev.Lifecycle.Cause)
			}
		}
	}
	os.Exit(exit)
}
