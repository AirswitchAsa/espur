// Package wechat implements the WeChat IM adapter via Tencent's personal-account
// iLink Bot API, using the SDK github.com/openilink/openilink-sdk-go (pinned at
// v0.6.0). This is a *personal* WeChat bot: it logs in by scanning a QR image
// from a phone and then long-polls iLink's HTTP/JSON endpoint for messages.
//
// It replaces the earlier Web/Desktop WeChat protocol (openwechat), which Tencent
// actively bans. iLink is the officially sanctioned personal-bot transport.
//
// Transport semantics (see docs/specs/adapter.dog.md "Platform notes — WeChat"):
//   - Login: QR scan -> bot_token (the QR is an image payload, not a
//     login.weixin.qq.com URL).
//   - Inbound: long-poll via Monitor; each reply must echo the inbound
//     message's context_token.
//   - Outbound: reply-only. The adapter caches the latest context_token per
//     thread and threads it into the send call. A Post to a thread with no
//     cached token returns adapter.ErrThreadGone.
//   - DMs are the only supported conversation. Tencent's grayscale ClawBot does
//     not deliver group messages, so inbound group messages are dropped and the
//     adapter never sends to a group. Group support waits on the platform.
package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mdp/qrterminal/v3"
	ilink "github.com/openilink/openilink-sdk-go"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/adapter/textchunk"
)

// MaxChunk is the safe length we send to iLink in one text message. The wire
// protocol tolerates more, but we mirror Discord's hard-split behaviour for
// code-fenced output and leave headroom; 1800 is comfortable.
const MaxChunk = 1800

// ilinkClient is the slice of the iLink SDK the adapter depends on. Defining it
// here lets wechat_test.go substitute a fake and assert behaviour without
// touching the network. *ilink.Client satisfies it.
type ilinkClient interface {
	LoginWithQR(ctx context.Context, cb *ilink.LoginCallbacks) (*ilink.LoginResult, error)
	Monitor(ctx context.Context, handler ilink.MessageHandler, opts *ilink.MonitorOptions) error
	SendText(ctx context.Context, to, text, contextToken string) (string, error)
	SetBaseURL(url string)
}

// session is the persisted boot state. Replaces the openwechat hot-reload blob.
type session struct {
	BotToken string `json:"bot_token"`
	BotID    string `json:"bot_id"`
	UserID   string `json:"user_id"`
	BaseURL  string `json:"base_url"`
	Cursor   string `json:"cursor"`
}

// Adapter implements adapter.Adapter for personal WeChat over iLink.
type Adapter struct {
	store    SessionStore // persists the session blob; see session_store.go
	baseURL  string       // optional WithBaseURL override (tests / self-host)
	platform string       // routing key returned by Platform(); see WithPlatformKey

	// newClient builds the SDK client for a given bot token. Injectable so
	// tests can supply a fake; defaults to a real *ilink.Client in New.
	newClient func(token string) ilinkClient

	// qrCallback surfaces the login QR image content to the operator. nil ->
	// default (emit a structured line on stdout). Injectable for tests.
	qrCallback func(imgContent string)

	mu      sync.Mutex
	client  ilinkClient
	threads map[string]string // threadID (FromUserID) -> latest context_token
	sess    session

	events  chan adapter.Event
	healthy atomic.Bool
}

// Option configures an Adapter at construction.
type Option func(*Adapter)

// WithBaseURL points the SDK client at a non-default iLink base URL (local
// testing / self-host). Empty is ignored.
func WithBaseURL(url string) Option {
	return func(a *Adapter) { a.baseURL = strings.TrimSpace(url) }
}

// WithPlatformKey overrides the routing key returned by Platform(). The
// connection manager sets this to the connection's composite identity
// ("wechat:<id>") so multiple WeChat connections stay isolated across dedup,
// transcripts, thread dirs, and outbound routing. Defaults to the bare
// "wechat" when unset (single-connection / legacy).
func WithPlatformKey(key string) Option {
	return func(a *Adapter) {
		if k := strings.TrimSpace(key); k != "" {
			a.platform = k
		}
	}
}

// withClient injects a client factory. Test-only.
func withClient(factory func(token string) ilinkClient) Option {
	return func(a *Adapter) { a.newClient = factory }
}

// New constructs an unstarted WeChat adapter. store persists the post-login
// session (bot token + base URL + poll cursor) so subsequent boots skip the QR
// scan; use NewFileStore for a file, or a vault-backed store for a web-managed
// connection.
func New(store SessionStore, opts ...Option) (*Adapter, error) {
	if store == nil {
		return nil, errors.New("wechat: session store is required")
	}
	a := &Adapter{
		store:    store,
		platform: "wechat",
		threads:  make(map[string]string),
	}
	for _, o := range opts {
		o(a)
	}
	if a.newClient == nil {
		a.newClient = func(token string) ilinkClient {
			var copts []ilink.Option
			if a.baseURL != "" {
				copts = append(copts, ilink.WithBaseURL(a.baseURL))
			}
			return ilink.NewClient(token, copts...)
		}
	}
	return a, nil
}

func (a *Adapter) Platform() string { return a.platform }
func (a *Adapter) Healthy() bool    { return a.healthy.Load() }

// SetQRCallback overrides what happens when iLink emits the login QR image. The
// default emits a structured line on stdout; cmd/espur wires this to the
// structured logger, and tests can capture it.
func (a *Adapter) SetQRCallback(fn func(imgContent string)) { a.qrCallback = fn }

// Start launches the login/long-poll loop on a background goroutine and returns
// the inbound event channel immediately. Transport outcomes (login success,
// session expiry, disconnect) are surfaced as LifecycleEvents rather than as a
// Start error, per docs/specs/adapter.dog.md. The channel closes when ctx is
// cancelled or the loop terminates.
func (a *Adapter) Start(ctx context.Context) (<-chan adapter.Event, error) {
	a.events = make(chan adapter.Event, 16)
	go a.run(ctx)
	return a.events, nil
}

// run owns login + Monitor. All emits and channel close happen on this single
// goroutine (Monitor invokes the handler synchronously on the same goroutine),
// so there is no concurrent send-after-close.
func (a *Adapter) run(ctx context.Context) {
	defer close(a.events)
	// Backstop: a panic anywhere in login/Monitor must not crash the process and
	// must still close the events channel (deferred above) so connman's fan-in
	// unblocks and the connection is torn down cleanly.
	defer func() {
		if p := recover(); p != nil {
			logLine("wechat.run.panic", map[string]any{"panic": fmt.Sprint(p)})
			a.healthy.Store(false)
			a.emit(a.lifecycle(adapter.LifecycleDisconnected, "recovered panic", 0))
		}
	}()

	sess := a.loadSession()
	client := a.newClient(sess.BotToken)
	if sess.BaseURL != "" {
		client.SetBaseURL(sess.BaseURL)
	}
	a.mu.Lock()
	a.client = client
	a.sess = sess
	a.mu.Unlock()

	if sess.BotToken == "" {
		// Fresh login: QR scan -> bot_token. LoginWithQR has its own internal
		// timeout, so this does not hang forever waiting on a human.
		res, err := client.LoginWithQR(ctx, &ilink.LoginCallbacks{
			OnQRCode:  a.handleQRCode,
			OnScanned: func() { logLine("wechat.login.scanned", nil) },
			OnExpired: func(attempt, max int) {
				logLine("wechat.login.qr_expired", map[string]any{"attempt": attempt, "max": max})
			},
		})
		if err != nil {
			a.healthy.Store(false)
			a.emit(a.lifecycle(adapter.LifecycleDisconnected, err.Error(), 0))
			return
		}
		if !res.Connected {
			// Login flow ended without connecting (timeout / too many expiries).
			a.emit(a.lifecycle(adapter.LifecycleAuthRevoked, res.Message, 0))
			return
		}
		sess.BotToken = res.BotToken
		sess.BotID = res.BotID
		sess.UserID = res.UserID
		sess.BaseURL = res.BaseURL
		if err := a.saveSession(sess); err != nil {
			logLine("wechat.session.persist_failed", map[string]any{"err": err.Error()})
		}
	}

	a.mu.Lock()
	a.sess = sess
	a.mu.Unlock()

	a.healthy.Store(true)
	a.emit(a.lifecycle(adapter.LifecycleConnected, "", 0))

	// Monitor runs on its own cancellable context so session expiry can stop it
	// promptly. Without this, the SDK handles an expired session by sleeping
	// (~1h) and looping, leaving this adapter "running" but dead for an hour.
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	var sessionExpired atomic.Bool

	err := client.Monitor(monitorCtx, a.onMessage, &ilink.MonitorOptions{
		InitialBuf:  sess.Cursor,
		OnBufUpdate: a.saveCursor,
		OnError: func(err error) {
			logLine("wechat.poll.error", map[string]any{"err": err.Error()})
		},
		OnSessionExpired: func() {
			sessionExpired.Store(true)
			a.healthy.Store(false)
			a.emit(a.lifecycle(adapter.LifecycleAuthRevoked, "session expired", 0))
			// Wipe the saved token so the next boot forces a fresh QR scan, and
			// cancel Monitor so it returns now instead of sleeping and retrying.
			a.clearAuth()
			cancelMonitor()
		},
	})

	a.healthy.Store(false)
	if ctx.Err() == nil && !sessionExpired.Load() {
		// Monitor returned without a parent cancellation or a handled session
		// expiry — an unexpected terminal exit.
		cause := ""
		if err != nil {
			cause = err.Error()
		}
		a.emit(a.lifecycle(adapter.LifecycleDisconnected, cause, 0))
	}
}

// handleQRCode surfaces the login QR image for the operator.
func (a *Adapter) handleQRCode(imgContent string) {
	if a.qrCallback != nil {
		a.qrCallback(imgContent)
		return
	}
	logLine("wechat.login.qr", map[string]any{"qr_img_content": imgContent})
}

// WriteLoginQR renders the iLink login payload for the operator to scan. The
// payload (qrcode_img_content) is a URL that WeChat expects encoded in a QR, so
// we draw a terminal QR and also print the raw URL as a guaranteed fallback (in
// case the terminal rendering is unscannable, e.g. wrong contrast — paste it
// into any QR generator, or some clients open it directly). cmd/espur wires
// this into SetQRCallback.
func WriteLoginQR(w io.Writer, content string) {
	fmt.Fprintln(w, "\n[wechat] Scan this QR with the WeChat mobile app to log in:")
	qrterminal.GenerateHalfBlock(content, qrterminal.M, w)
	fmt.Fprintf(w, "\n[wechat] If the QR won't scan, encode this URL instead:\n%s\n\n", content)
}

func (a *Adapter) lifecycle(kind adapter.LifecycleKind, cause string, attempt int) adapter.Event {
	return adapter.Event{Lifecycle: &adapter.LifecycleEvent{
		Platform: a.Platform(), Kind: kind, Cause: cause, Attempt: attempt, At: time.Now(),
	}}
}

// emitBudget is the max time emit will wait to push onto the inbound channel.
// Package-level so tests can shrink it. Mirrors the Discord adapter.
var emitBudget = time.Second

func (a *Adapter) emit(ev adapter.Event) {
	select {
	case a.events <- ev:
		return
	case <-time.After(emitBudget):
		// Channel backpressure. Surface a Disconnected{cause="downstream
		// backpressure"} so the operator sees the drop, per the adapter spec.
	}
	dropEv := a.lifecycle(adapter.LifecycleDisconnected, "downstream backpressure", 0)
	select {
	case a.events <- dropEv:
	case <-time.After(emitBudget):
		// Channel fully wedged; nowhere to put the signal.
	}
}

// onMessage runs on Monitor's goroutine. It drops the bot's own echoes and
// group messages, normalizes the body, caches the reply token, and emits a
// MessageEvent. The thread is the DM peer (ThreadID -> FromUserID); a DM is
// always an implicit mention, like Discord.
func (a *Adapter) onMessage(msg ilink.WeixinMessage) {
	// Monitor invokes this synchronously on the run goroutine; a panic here would
	// otherwise unwind through Monitor and tear down the whole connection. Contain
	// it to this one message so the poll loop keeps running.
	defer func() { _ = recover() }()
	// Self-echo: the bot's own outbound (and any bot-generated message)
	// arrives with MessageType == MsgTypeBot; drop it so replies never loop.
	// We deliberately do NOT filter on the login UserID: that id is the human
	// operator who owns the bot (an "@im.wechat" id), distinct from the bot's
	// own "@im.bot" BotID — so the operator's own messages to the bot are
	// legitimate inbound and must pass through.
	if msg.MessageType == ilink.MsgTypeBot {
		return
	}
	// Groups are unsupported (Tencent grayscale does not deliver them, and the
	// adapter never sends to one). Drop defensively if one ever arrives.
	if msg.GroupID != "" {
		return
	}

	threadID := msg.FromUserID
	body := normalizeMessage(&msg)

	// Cache the latest context_token for this thread so Post can echo it.
	if msg.ContextToken != "" {
		a.mu.Lock()
		a.threads[threadID] = msg.ContextToken
		a.mu.Unlock()
	}

	a.emit(adapter.Event{Message: &adapter.MessageEvent{
		Platform:          a.Platform(),
		ThreadID:          threadID,
		PlatformMessageID: strconv.FormatInt(msg.MessageID, 10),
		Author:            adapter.Author{ID: msg.FromUserID, Label: msg.FromUserID},
		Body:              body,
		Mention:           true, // DM is an implicit mention, like Discord.
		ReceivedAt:        time.Now(),
	}})
}

// mentionTrailer matches the "@nickname " token WeChat renders for a mention
// (the trailing rune is a four-per-em space, U+2005).
var mentionTrailer = regexp.MustCompile(`@([^\s\x{2005}]+)\x{2005}?`)

// normalizeMessage extracts the body: the message text, or a media placeholder
// when the message carries no text.
func normalizeMessage(msg *ilink.WeixinMessage) string {
	text := ilink.ExtractText(msg)
	if text == "" {
		text = mediaPlaceholder(msg)
	}
	return normalizeBody(text)
}

// mediaPlaceholder renders attachment-only messages to a placeholder token,
// mirroring the Discord adapter's "[image]" / "[attachment]" handling.
func mediaPlaceholder(msg *ilink.WeixinMessage) string {
	var parts []string
	for _, item := range msg.ItemList {
		switch item.Type {
		case ilink.ItemImage:
			parts = append(parts, "[image]")
		case ilink.ItemVideo:
			parts = append(parts, "[video]")
		case ilink.ItemVoice:
			parts = append(parts, "[voice]")
		case ilink.ItemFile:
			parts = append(parts, "[file]")
		}
	}
	return strings.Join(parts, " ")
}

// normalizeBody strips stray "@xxx" mention tokens from the body as
// ornamentation. iLink renders mentions only as text, never structurally.
func normalizeBody(content string) string {
	cleaned := mentionTrailer.ReplaceAllString(content, "")
	return strings.TrimSpace(cleaned)
}

// Post sends body to the WeChat thread identified by threadID. It splits into
// MaxChunk-sized chunks via textchunk.Split, echoes the cached context_token,
// and returns the first chunk's id for transcript correlation. A thread with no
// cached token (reply-only: peer never messaged us) yields ErrThreadGone.
func (a *Adapter) Post(ctx context.Context, threadID, body string) (string, error) {
	a.mu.Lock()
	client := a.client
	token, ok := a.threads[threadID]
	a.mu.Unlock()
	if client == nil {
		return "", errors.New("wechat: not started")
	}
	if !ok {
		// No cached context token: we cannot initiate, only reply.
		return "", adapter.ErrThreadGone
	}

	chunks := textchunk.Split(body, MaxChunk)
	if len(chunks) == 0 {
		return "", nil
	}
	var firstID string
	for i, ch := range chunks {
		select {
		case <-ctx.Done():
			return firstID, ctx.Err()
		default:
		}
		id, err := sendWithRetry(ctx, client, threadID, ch, token)
		if err != nil {
			if isThreadGone(err) {
				return firstID, adapter.ErrThreadGone
			}
			return firstID, err
		}
		if i == 0 {
			firstID = id
		}
	}
	return firstID, nil
}

// postBackoff is the per-attempt wait schedule for transient outbound failures,
// so a single transient send error doesn't drop the whole reply. Spec:
// adapter.dog.md "Outbound". A var so tests can shrink it.
var postBackoff = []time.Duration{1 * time.Second, 3 * time.Second, 9 * time.Second}

// sendWithRetry sends one chunk via the given client, retrying transient
// failures with bounded backoff. A thread-gone error (expired context token) is
// terminal.
func sendWithRetry(ctx context.Context, cl ilinkClient, threadID, text, token string) (string, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		id, err := cl.SendText(ctx, threadID, text, token)
		if err == nil {
			return id, nil
		}
		if isThreadGone(err) {
			return "", err // terminal; caller maps to ErrThreadGone
		}
		lastErr = err
		if attempt >= len(postBackoff) {
			return "", lastErr
		}
		select {
		case <-time.After(postBackoff[attempt]):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// isThreadGone maps the SDK's "no cached token" sentinel to thread-gone
// semantics. (We normally catch the unknown-thread case via our own cache, but
// a token can expire between inbound and Post.)
func isThreadGone(err error) bool {
	return errors.Is(err, ilink.ErrNoContextToken)
}

// ---- persistence ----

// loadSession reads the persisted session via the store. A missing or
// unreadable session yields a zero session (fresh QR login), never an error.
func (a *Adapter) loadSession() session {
	data, err := a.store.Load()
	if err != nil {
		logLine("wechat.session.load_failed", map[string]any{"err": err.Error()})
		return session{}
	}
	if len(data) == 0 {
		return session{}
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil {
		logLine("wechat.session.parse_failed", map[string]any{"err": err.Error()})
		return session{}
	}
	return s
}

// saveSession marshals and persists the session via the store.
func (a *Adapter) saveSession(s session) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return a.store.Save(data)
}

// saveCursor persists the advancing long-poll cursor. Invoked from Monitor's
// OnBufUpdate as new sync state arrives.
func (a *Adapter) saveCursor(buf string) {
	a.mu.Lock()
	a.sess.Cursor = buf
	s := a.sess
	a.mu.Unlock()
	if err := a.saveSession(s); err != nil {
		logLine("wechat.session.persist_failed", map[string]any{"err": err.Error()})
	}
}

// clearAuth wipes the persisted credentials (keeping the file empty) so the
// next boot performs a fresh QR login. Called on session expiry.
func (a *Adapter) clearAuth() {
	a.mu.Lock()
	a.sess = session{}
	s := a.sess
	a.mu.Unlock()
	if err := a.saveSession(s); err != nil {
		logLine("wechat.session.persist_failed", map[string]any{"err": err.Error()})
	}
}

// logLine emits a best-effort structured line on stdout for the few transport
// callbacks the SDK exposes. The adapter has no slog handle of its own; the
// operator-facing QR surface is injectable via SetQRCallback.
func logLine(event string, fields map[string]any) {
	rec := map[string]any{"event": event, "platform": "wechat"}
	for k, v := range fields {
		rec[k] = v
	}
	if b, err := json.Marshal(rec); err == nil {
		fmt.Println(string(b))
	}
}
