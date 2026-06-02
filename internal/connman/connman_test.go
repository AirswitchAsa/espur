package connman

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/bot"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
	"github.com/punny/espur/internal/web"
)

// fakeAdapter is a network-free adapter for exercising the manager's runtime
// start/stop lifecycle. It closes its event channel when its Start ctx is
// cancelled, which is what lets Manager.stop drain cleanly.
type fakeAdapter struct {
	platform string
	ch       chan adapter.Event
	healthy  atomic.Bool
}

func (f *fakeAdapter) Platform() string { return f.platform }
func (f *fakeAdapter) Healthy() bool    { return f.healthy.Load() }
func (f *fakeAdapter) Post(context.Context, string, string) (string, error) {
	return "", nil
}
func (f *fakeAdapter) Start(ctx context.Context) (<-chan adapter.Event, error) {
	f.healthy.Store(true)
	go func() {
		<-ctx.Done()
		close(f.ch)
	}()
	return f.ch, nil
}

func newTestManager(t *testing.T) (*Manager, *store.DB, *secrets.Vault) {
	t.Helper()
	db, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	key, err := secrets.GenerateIdentity()
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	vault, err := secrets.New(key)
	if err != nil {
		t.Fatalf("vault: %v", err)
	}
	pool := vendor.New(db, vault)
	ts := transcript.NewStore(t.TempDir())
	core := bot.New(bot.Config{DB: db, Pool: pool, Transcript: ts})
	srv := web.New(db, vault, pool, ts)
	return New(db, vault, core, srv, nil), db, vault
}

func TestMigrate_SeedsAndImportsWeChatSession(t *testing.T) {
	ctx := context.Background()
	m, db, vault := newTestManager(t)

	dataDir := t.TempDir()
	sessionPath := filepath.Join(dataDir, "wechat-session.json")
	sessionBytes := []byte(`{"bot_token":"tok","bot_id":"b@im.bot","cursor":"c"}`)
	if err := os.WriteFile(sessionPath, sessionBytes, 0o600); err != nil {
		t.Fatalf("write legacy session: %v", err)
	}

	seeded, err := m.Migrate(ctx, dataDir, "discord-token", true)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if seeded != 2 {
		t.Fatalf("seeded=%d want 2", seeded)
	}

	// Both connections exist with the BARE platform key as id.
	for _, id := range []string{"discord", "wechat"} {
		c, err := db.GetConnection(ctx, id)
		if err != nil {
			t.Fatalf("connection %q missing: %v", id, err)
		}
		if !c.Enabled {
			t.Fatalf("connection %q should be enabled", id)
		}
	}

	// Discord token is stored encrypted under the connection scope.
	cred, err := db.GetCredential(ctx, store.ConnScope, "discord")
	if err != nil {
		t.Fatalf("discord cred missing: %v", err)
	}
	plain, err := vault.Decrypt(cred.Blob)
	if err != nil || string(plain) != "discord-token" {
		t.Fatalf("discord token roundtrip: %q err=%v", plain, err)
	}

	// The legacy plaintext session file is imported into the vault and removed.
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("legacy session file should be removed, stat err=%v", err)
	}
	ss := &vaultSessionStore{db: db, vault: vault, id: "wechat"}
	got, err := ss.Load()
	if err != nil {
		t.Fatalf("load imported session: %v", err)
	}
	if string(got) != string(sessionBytes) {
		t.Fatalf("imported session mismatch: %q", got)
	}

	// Migrate is a no-op once any connection exists.
	seeded2, err := m.Migrate(ctx, dataDir, "other-token", true)
	if err != nil {
		t.Fatalf("migrate again: %v", err)
	}
	if seeded2 != 0 {
		t.Fatalf("second migrate should seed 0, got %d", seeded2)
	}
}

func TestMigrate_EmptyEnvSeedsNothing(t *testing.T) {
	ctx := context.Background()
	m, _, _ := newTestManager(t)
	seeded, err := m.Migrate(ctx, t.TempDir(), "", false)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if seeded != 0 {
		t.Fatalf("seeded=%d want 0", seeded)
	}
}

func TestRuntimeLifecycle_StartListStop(t *testing.T) {
	ctx := context.Background()
	m, db, _ := newTestManager(t)

	// Inject a fake adapter so we exercise register/unregister without network.
	m.buildFn = func(c store.Connection, r *running) (adapter.Adapter, error) {
		return &fakeAdapter{platform: c.ID, ch: make(chan adapter.Event, 1)}, nil
	}

	base, cancelBase := context.WithCancel(ctx)
	defer cancelBase()
	if err := m.Boot(base); err != nil {
		t.Fatalf("boot: %v", err)
	}

	// Add a connection at runtime; it should start and register.
	if err := m.AddDiscord(ctx, "tok"); err != nil {
		t.Fatalf("add discord: %v", err)
	}
	list, _ := m.List(ctx)
	if len(list) != 1 {
		t.Fatalf("want 1 connection, got %d", len(list))
	}
	id := list[0].ID
	if !list[0].Healthy {
		t.Fatal("started connection should report healthy")
	}

	// Registered with the core (outbound routing) and the manager tracks it.
	m.mu.Lock()
	_, running := m.running[id]
	m.mu.Unlock()
	if !running {
		t.Fatal("connection not tracked as running")
	}

	// Disable stops it (drains the fan-in) and unregisters.
	done := make(chan error, 1)
	go func() { done <- m.Disable(ctx, id) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("disable: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Disable did not return — fan-in failed to drain")
	}

	m.mu.Lock()
	_, stillRunning := m.running[id]
	m.mu.Unlock()
	if stillRunning {
		t.Fatal("connection should no longer be running after disable")
	}
	// DB row persists but is disabled.
	c, err := db.GetConnection(ctx, id)
	if err != nil || c.Enabled {
		t.Fatalf("connection should be disabled-not-deleted: %+v err=%v", c, err)
	}
}

func TestVaultSessionStore_RoundtripAndAbsent(t *testing.T) {
	_, db, vault := newTestManager(t)
	ss := &vaultSessionStore{db: db, vault: vault, id: "wechat:x"}

	// Absent → (nil, nil).
	got, err := ss.Load()
	if err != nil || got != nil {
		t.Fatalf("absent load: %q err=%v", got, err)
	}
	if err := ss.Save([]byte("hello")); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = ss.Load()
	if err != nil || string(got) != "hello" {
		t.Fatalf("roundtrip: %q err=%v", got, err)
	}
}
