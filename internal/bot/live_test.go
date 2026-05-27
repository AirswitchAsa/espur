package bot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
)

// TestBot_Live_HappyPath_DeepSeek wires a real opencode invocation through
// the full trigger pipeline (bot core → context-assembly → vendor pool →
// invoker → reply → transcript) and asserts a non-empty reply gets posted.
// This is the strongest non-IM end-to-end test we have. Gated by
// ESPUR_OPENCODE_LIVE=1; DeepSeek used because it's the provider the dev
// box has authed in ~/.local/share/opencode/auth.json.
//
// On the user's terms: ~5s and ~$0.0001 of DeepSeek tokens per run.
func TestBot_Live_HappyPath_DeepSeek(t *testing.T) {
	if os.Getenv("ESPUR_OPENCODE_LIVE") == "" {
		t.Skip("set ESPUR_OPENCODE_LIVE=1 to run live bot pipeline test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode CLI not on PATH: %v", err)
	}
	authPath := filepath.Join(os.Getenv("HOME"), ".local/share/opencode/auth.json")
	if _, err := os.Stat(authPath); err != nil {
		t.Skipf("no opencode auth.json at %s: %v", authPath, err)
	}

	core, fa, _ := newLiveCore(t, "deepseek-live", "deepseek/deepseek-chat", "")

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	core.Dispatch(ctx, adapter.Event{Message: &adapter.MessageEvent{
		Platform:          "discord",
		ThreadID:          "live-thread",
		PlatformMessageID: "live-m-1",
		Author:            adapter.Author{ID: "alice", Label: "alice"},
		Body:              "Reply with exactly the phrase: PING_OK",
		Mention:           true,
		ReceivedAt:        time.Now(),
	}})

	select {
	case body := <-fa.done:
		if strings.TrimSpace(body) == "" {
			t.Fatalf("empty reply posted")
		}
		t.Logf("live reply: %q", body)
	case <-ctx.Done():
		t.Fatalf("never got a Post within 90s: %v", ctx.Err())
	}
}

// TestBot_Live_PoolFallthrough_DeepSeek configures a deliberately-broken
// first vendor (bogus model id) followed by DeepSeek. Asserts the pool
// classifies the first attempt's upstream error, penalizes it, and the
// second attempt succeeds. Exercises the auth/rate-limit classify path
// against real upstream error JSON, not stubbed text.
func TestBot_Live_PoolFallthrough_DeepSeek(t *testing.T) {
	if os.Getenv("ESPUR_OPENCODE_LIVE") == "" {
		t.Skip("set ESPUR_OPENCODE_LIVE=1 to run live pool-fallthrough test")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skipf("opencode CLI not on PATH: %v", err)
	}

	// "anthropic/this-model-does-not-exist" reliably triggers an upstream
	// 4xx with no resemblance to a real auth/rate-limit phrase. The Classify
	// table won't bucket it; the pool surfaces it as Crash from the first
	// vendor and stops. So instead we use a model name with a provider
	// suffix that triggers a usage-limit / rate-limit shaped error.
	//
	// Easier path: configure the broken vendor with a model under a
	// provider opencode doesn't have auth for. opencode returns
	// "authentication_error" / "unauthorized" → ClassAuth → fallthrough.
	core, fa, db := newLiveCore(t,
		"unauthed-broken", "openai/gpt-4o-mini", "")
	ctx := context.Background()
	// Append DeepSeek as the fallthrough target.
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: "deepseek-live", Model: "deepseek/deepseek-chat",
		Enabled: true, Position: 1, CredKind: "byo_key",
	})

	tctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	core.Dispatch(tctx, adapter.Event{Message: &adapter.MessageEvent{
		Platform:          "discord",
		ThreadID:          "live-fallthrough",
		PlatformMessageID: "live-m-fall",
		Author:            adapter.Author{ID: "alice", Label: "alice"},
		Body:              "Say PONG and nothing else.",
		Mention:           true,
		ReceivedAt:        time.Now(),
	}})

	select {
	case body := <-fa.done:
		t.Logf("live fallthrough reply: %q", body)
		if strings.TrimSpace(body) == "" {
			t.Fatalf("empty reply posted")
		}
	case <-tctx.Done():
		t.Fatalf("never got a Post within 120s: %v", tctx.Err())
	}

	// The broken vendor MUST be penalized (auth_locked or cooldown). If
	// it's still eligible we either misclassified or never fell through.
	pen, err := db.GetPenalty(context.Background(), "unauthed-broken")
	if err != nil {
		t.Fatal(err)
	}
	if pen.Status == store.PenaltyEligible {
		t.Fatalf("broken vendor was not penalized: %+v", pen)
	}
	t.Logf("broken vendor status: %s", pen.Status)
}

// newLiveCore is like newCore (bot_test.go) but skips the WithInvoker call so
// the real opencode invoker runs. Adds a single vendor row matching what the
// caller wants tried first.
func newLiveCore(t *testing.T, vendorID, model, _ string) (*Core, *fakeAdapter, *store.DB) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := store.Open(filepath.Join(dataDir, "espur.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	key, _ := secrets.GenerateIdentity()
	vault, _ := secrets.New(key)
	pool := vendor.New(db, vault)
	ts := transcript.NewStore(dataDir)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: vendorID, Model: model, Enabled: true, Position: 0, CredKind: "byo_key",
	})
	core := New(Config{
		DB: db, Pool: pool, Transcript: ts,
		DashboardURL: "http://dashboard.local", InvokeTimeout: 60 * time.Second,
	})
	fa := &fakeAdapter{done: make(chan string, 4)}
	core.RegisterAdapter(fa)
	return core, fa, db
}
