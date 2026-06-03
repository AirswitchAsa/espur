// Package connman is the connection manager: it owns the runtime lifecycle of
// adapter instances ("connections"). Connections are persisted in the database
// (see internal/store) and created, enabled, disabled, and deleted at runtime
// from the web UI; the manager starts and stops adapters to match, registering
// each with the bot core (outbound routing) and the web server (status).
//
// A connection's identity is its composite routing key kind:id (or the bare
// platform key for the migrated legacy connection). That key is what the rest
// of Espur sees as Platform(), so multiple connections of the same platform are
// fully isolated. See docs/specs/adapter.dog.md "Connection identity".
package connman

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/adapter/discord"
	"github.com/punny/espur/internal/adapter/wechat"
	"github.com/punny/espur/internal/bot"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/web"
)

// Supported connection kinds.
const (
	KindDiscord = "discord"
	KindWeChat  = "wechat"
)

// Runtime states surfaced to the web UI.
const (
	StateStopped      = "stopped"
	StateStarting     = "starting"
	StateQR           = "qr" // WeChat login QR pending a scan
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
	StateAuthRevoked  = "auth_revoked"
)

// Manager owns connection lifecycle. Construct with New, then call Boot once at
// startup. Add/Enable/Disable/Delete drive runtime changes from the web UI.
type Manager struct {
	db     *store.DB
	vault  *secrets.Vault
	core   *bot.Core
	web    *web.Server
	logger *slog.Logger

	mu      sync.Mutex
	baseCtx context.Context // parent for every connection; cancelled on shutdown
	running map[string]*running

	// buildFn overrides adapter construction in tests; nil uses build().
	buildFn func(store.Connection, *running) (adapter.Adapter, error)
}

// running is the live state of one started connection.
type running struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu    sync.Mutex
	ad    adapter.Adapter // nil during the start window before the adapter is built
	state string
	qr    string
}

func (r *running) snapshot() (state, qr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state, r.qr
}

func (r *running) set(state, qr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.state = state
	r.qr = qr
}

func (r *running) setAdapter(ad adapter.Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ad = ad
}

// healthy reports the adapter's health, treating the pre-build start window
// (ad still nil) as not-yet-healthy rather than dereferencing nil.
func (r *running) healthy() bool {
	r.mu.Lock()
	ad := r.ad
	r.mu.Unlock()
	return ad != nil && ad.Healthy()
}

// New constructs a Manager.
func New(db *store.DB, vault *secrets.Vault, core *bot.Core, webSrv *web.Server, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		db:      db,
		vault:   vault,
		core:    core,
		web:     webSrv,
		logger:  logger,
		running: map[string]*running{},
	}
}

// Boot records the base context and starts every enabled connection. The base
// context must be the adapter context cancelled at shutdown phase 1.
func (m *Manager) Boot(ctx context.Context) error {
	m.mu.Lock()
	m.baseCtx = ctx
	m.mu.Unlock()

	conns, err := m.db.ListConnections(ctx)
	if err != nil {
		return fmt.Errorf("connman: list connections: %w", err)
	}
	for _, c := range conns {
		if !c.Enabled {
			continue
		}
		if err := m.start(c); err != nil {
			m.logger.Error("connection start failed",
				"event", "connection.start_failed", "connection", c.ID, "err", err.Error())
		}
	}
	return nil
}

// Migrate seeds connections from the legacy env configuration the first time
// Espur boots after connections moved into the database. It is a no-op once any
// connection exists. The migrated connections use the BARE platform key as
// their id ("discord", "wechat") so existing thread history, transcripts, and
// dedup rows keep resolving. The legacy plaintext WeChat session file, if
// present, is imported into the encrypted vault and then removed. Returns the
// number of connections seeded.
func (m *Manager) Migrate(ctx context.Context, dataDir, discordToken string, wechatEnabled bool) (int, error) {
	n, err := m.db.CountConnections(ctx)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		return 0, nil // already configured; DB is the source of truth
	}

	seeded := 0
	if discordToken = strings.TrimSpace(discordToken); discordToken != "" {
		if err := m.putSecret(ctx, KindDiscord, "platform_token", []byte(discordToken)); err != nil {
			return seeded, err
		}
		if err := m.db.PutConnection(ctx, store.Connection{
			ID: KindDiscord, Kind: KindDiscord, Label: "Discord", Enabled: true,
		}); err != nil {
			return seeded, err
		}
		seeded++
	}

	if wechatEnabled {
		// Import the legacy plaintext session file into the vault, if any.
		legacy := filepath.Join(dataDir, "wechat-session.json")
		if data, rerr := os.ReadFile(legacy); rerr == nil && len(data) > 0 {
			ss := &vaultSessionStore{db: m.db, vault: m.vault, id: KindWeChat}
			if err := ss.Save(data); err != nil {
				return seeded, err
			}
			// The session now lives encrypted in the vault; the plaintext copy on
			// disk is exactly the exposure migration exists to remove. Try to
			// unlink it; if that fails, at least scrub its contents, and if even
			// that fails, fail boot loudly rather than run with a readable secret
			// sitting on disk.
			if err := os.Remove(legacy); err != nil {
				if werr := os.WriteFile(legacy, []byte{}, 0o600); werr != nil {
					return seeded, fmt.Errorf("connman: imported legacy wechat session but could not remove or scrub plaintext file %s: %w",
						legacy, errors.Join(err, werr))
				}
				m.logger.Warn("scrubbed legacy wechat session content but could not unlink the file",
					"event", "connection.migrate", "path", legacy, "err", err.Error())
			}
		}
		if err := m.db.PutConnection(ctx, store.Connection{
			ID: KindWeChat, Kind: KindWeChat, Label: "WeChat", Enabled: true,
		}); err != nil {
			return seeded, err
		}
		seeded++
	}
	return seeded, nil
}

// List returns every configured connection merged with its runtime status.
func (m *Manager) List(ctx context.Context) ([]web.ConnInfo, error) {
	conns, err := m.db.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]web.ConnInfo, 0, len(conns))
	for _, c := range conns {
		v := web.ConnInfo{ID: c.ID, Kind: c.Kind, Label: c.Label, Enabled: c.Enabled, State: StateStopped}
		m.mu.Lock()
		r := m.running[c.ID]
		m.mu.Unlock()
		if r != nil {
			v.State, v.QR = r.snapshot()
			v.Healthy = r.healthy()
		}
		out = append(out, v)
	}
	return out, nil
}

// Status returns the runtime state and pending QR (if any) for one connection.
func (m *Manager) Status(id string) (state, qr string) {
	m.mu.Lock()
	r := m.running[id]
	m.mu.Unlock()
	if r == nil {
		return StateStopped, ""
	}
	return r.snapshot()
}

// AddDiscord creates and starts a new Discord connection from a bot token.
func (m *Manager) AddDiscord(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return errors.New("connman: discord token is required")
	}
	id := newID(KindDiscord)
	if err := m.putSecret(ctx, id, "platform_token", []byte(token)); err != nil {
		return err
	}
	conn := store.Connection{ID: id, Kind: KindDiscord, Label: "Discord", Enabled: true}
	if err := m.db.PutConnection(ctx, conn); err != nil {
		return err
	}
	return m.start(conn)
}

// AddWeChat creates and starts a new WeChat connection. The login QR surfaces
// through the connection's runtime status (poll Status); on a successful scan
// the adapter persists its session to the encrypted credentials table.
func (m *Manager) AddWeChat(ctx context.Context) error {
	id := newID(KindWeChat)
	conn := store.Connection{ID: id, Kind: KindWeChat, Label: "WeChat", Enabled: true}
	if err := m.db.PutConnection(ctx, conn); err != nil {
		return err
	}
	return m.start(conn)
}

// Enable turns a connection on and starts it.
func (m *Manager) Enable(ctx context.Context, id string) error {
	conn, err := m.db.GetConnection(ctx, id)
	if err != nil {
		return err
	}
	if err := m.db.SetConnectionEnabled(ctx, id, true); err != nil {
		return err
	}
	conn.Enabled = true
	return m.start(conn)
}

// Disable stops a connection and marks it disabled (its credential/session is
// retained so it can be re-enabled without re-login).
func (m *Manager) Disable(ctx context.Context, id string) error {
	m.stop(id)
	return m.db.SetConnectionEnabled(ctx, id, false)
}

// Delete stops a connection and removes it and its stored secret entirely.
func (m *Manager) Delete(ctx context.Context, id string) error {
	m.stop(id)
	return m.db.DeleteConnection(ctx, id)
}

// start constructs the adapter for a connection, registers it with the core and
// web server, and launches its inbound fan-in. No-op if already running.
func (m *Manager) start(conn store.Connection) error {
	m.mu.Lock()
	if m.baseCtx == nil {
		m.mu.Unlock()
		return errors.New("connman: not booted")
	}
	if _, ok := m.running[conn.ID]; ok {
		m.mu.Unlock()
		return nil // already running
	}
	base := m.baseCtx
	// Reserve the slot now, before the slow build/Start, so a concurrent
	// stop(conn.ID) (Disable/Delete) observes this in-flight start and can cancel
	// it — rather than seeing nothing, no-op'ing, and leaving us to register a
	// live adapter the operator already removed.
	r := &running{state: StateStarting, done: make(chan struct{})}
	m.running[conn.ID] = r
	m.mu.Unlock()

	// fail rolls back the reservation and unblocks any stop() waiting on r.done.
	fail := func(err error) error {
		m.mu.Lock()
		if m.running[conn.ID] == r { // still ours (a racing stop may have removed it)
			delete(m.running, conn.ID)
		}
		m.mu.Unlock()
		close(r.done)
		return err
	}

	build := m.build
	if m.buildFn != nil {
		build = m.buildFn
	}
	ad, err := build(conn, r)
	if err != nil {
		return fail(err)
	}
	r.setAdapter(ad)

	rctx, cancel := context.WithCancel(base)
	r.cancel = cancel

	ch, err := ad.Start(rctx)
	if err != nil {
		cancel()
		return fail(fmt.Errorf("connman: start %s: %w", conn.ID, err))
	}

	// If a stop() raced in while we were starting, it removed our reservation
	// (and is waiting on r.done). Abort cleanly instead of registering: tear down
	// the adapter we just started and drain its channel.
	m.mu.Lock()
	if m.running[conn.ID] != r {
		m.mu.Unlock()
		cancel()
		go func() {
			for range ch { // discard; the cancelled adapter will close ch
			}
		}()
		close(r.done)
		m.logger.Info("connection start aborted by concurrent stop",
			"event", "connection.start_aborted", "connection", conn.ID)
		return nil
	}
	m.mu.Unlock()

	m.core.RegisterAdapter(ad)
	m.web.RegisterAdapter(ad)

	go m.fanIn(rctx, conn, r, ch)
	m.logger.Info("connection started",
		"event", "connection.started", "connection", conn.ID, "kind", conn.Kind)
	return nil
}

// build constructs the concrete adapter for a connection kind.
func (m *Manager) build(conn store.Connection, r *running) (adapter.Adapter, error) {
	switch conn.Kind {
	case KindDiscord:
		cred, err := m.db.GetCredential(context.Background(), store.ConnScope, conn.ID)
		if err != nil {
			return nil, fmt.Errorf("connman: discord %s has no token: %w", conn.ID, err)
		}
		token, err := m.vault.Decrypt(cred.Blob)
		if err != nil {
			return nil, fmt.Errorf("connman: decrypt discord token: %w", err)
		}
		return discord.New(string(token), discord.WithPlatformKey(conn.ID))

	case KindWeChat:
		ss := &vaultSessionStore{db: m.db, vault: m.vault, id: conn.ID}
		ad, err := wechat.New(ss, wechat.WithPlatformKey(conn.ID))
		if err != nil {
			return nil, err
		}
		ad.SetQRCallback(func(content string) { r.set(StateQR, content) })
		return ad, nil

	default:
		return nil, fmt.Errorf("connman: unknown connection kind %q", conn.Kind)
	}
}

// fanIn forwards inbound events to the core and tracks lifecycle for status.
func (m *Manager) fanIn(rctx context.Context, conn store.Connection, r *running, ch <-chan adapter.Event) {
	defer close(r.done)
	for ev := range ch {
		m.handleEvent(rctx, conn, r, ev)
	}
}

// handleEvent processes one inbound event with panic isolation: a panic in a
// lifecycle update or in core.Dispatch must not kill the fan-in goroutine (which
// would leave r.done unclosed and wedge a later stop() that waits on it). One
// bad event is logged and skipped; the connection keeps running.
func (m *Manager) handleEvent(rctx context.Context, conn store.Connection, r *running, ev adapter.Event) {
	defer func() {
		if p := recover(); p != nil {
			m.logger.Error("connection event handler panicked",
				"event", "connection.event_panic", "connection", conn.ID, "panic", fmt.Sprint(p))
		}
	}()
	if ev.Lifecycle != nil {
		switch ev.Lifecycle.Kind {
		case adapter.LifecycleConnected:
			r.set(StateConnected, "")
			m.deriveLabel(conn)
		case adapter.LifecycleDisconnected:
			r.set(StateDisconnected, "")
		case adapter.LifecycleAuthRevoked:
			r.set(StateAuthRevoked, "")
		}
	}
	m.core.Dispatch(rctx, ev)
}

// deriveLabel updates the human display label from the platform after connect.
// For WeChat the bot id is in the persisted session; Discord keeps its default.
func (m *Manager) deriveLabel(conn store.Connection) {
	if conn.Kind != KindWeChat {
		return
	}
	ss := &vaultSessionStore{db: m.db, vault: m.vault, id: conn.ID}
	data, err := ss.Load()
	if err != nil || len(data) == 0 {
		return
	}
	var s struct {
		BotID string `json:"bot_id"`
	}
	if err := json.Unmarshal(data, &s); err != nil || s.BotID == "" {
		return
	}
	label := "WeChat " + s.BotID
	if err := m.db.SetConnectionLabel(context.Background(), conn.ID, label); err != nil {
		m.logger.Warn("set connection label failed",
			"event", "connection.label_failed", "connection", conn.ID, "err", err.Error())
	}
}

// stop cancels a running connection, unregisters it, and waits for its fan-in
// to drain. No-op if not running.
func (m *Manager) stop(id string) {
	m.mu.Lock()
	r := m.running[id]
	delete(m.running, id)
	m.mu.Unlock()
	if r == nil {
		return
	}
	r.cancel()
	<-r.done
	m.core.UnregisterAdapter(id)
	m.web.UnregisterAdapter(id)
	m.logger.Info("connection stopped", "event", "connection.stopped", "connection", id)
}

// newID mints a composite connection id "kind:<6 hex>".
func newID(kind string) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return kind + ":" + hex.EncodeToString(b[:])
}

// putSecret encrypts and stores a connection secret in the credentials table.
func (m *Manager) putSecret(ctx context.Context, id, kind string, plaintext []byte) error {
	blob, err := m.vault.Encrypt(plaintext)
	if err != nil {
		return err
	}
	return m.db.PutCredential(ctx, store.Credential{
		Scope: store.ConnScope, ID: id, Kind: kind, Status: "set", Blob: blob,
	})
}
