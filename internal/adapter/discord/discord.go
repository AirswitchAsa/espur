// Package discord implements the Discord IM adapter. See docs/specs/adapter.dog.md
// — this package is the only place in Espur that knows Discord's wire format,
// mention semantics, and chunking limits.
package discord

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/punny/espur/internal/adapter"
)

// MaxChunk is Discord's documented per-message length cap.
const MaxChunk = 2000

// Adapter is the Discord implementation.
type Adapter struct {
	session  *discordgo.Session
	userID   string
	platform string // routing key returned by Platform(); see WithPlatformKey

	// typingFire triggers Discord's typing indicator on a channel. It is a field
	// (not a direct a.session call) so tests can substitute a counter without a
	// live session / network. Defaults in New to a.session.ChannelTyping.
	typingFire func(threadID string)

	mu      sync.Mutex
	events  chan adapter.Event
	healthy atomic.Bool
	typing  map[string]*adapter.TypingState // threadID -> active typing controller
}

// compile-time: the Discord adapter shows typing indicators.
var _ adapter.Typer = (*Adapter)(nil)

// Option configures an Adapter at construction.
type Option func(*Adapter)

// WithPlatformKey overrides the routing key returned by Platform(). The
// connection manager sets this to the connection's composite identity
// ("discord:<id>") so multiple Discord connections stay isolated across dedup,
// transcripts, thread dirs, and outbound routing. Defaults to the bare
// "discord" when unset (single-connection / legacy).
func WithPlatformKey(key string) Option {
	return func(a *Adapter) {
		if k := strings.TrimSpace(key); k != "" {
			a.platform = k
		}
	}
}

// New constructs an unstarted Discord adapter. token must be a bot token
// (decrypted from secrets just before construction; the adapter does not
// look it up itself).
func New(token string, opts ...Option) (*Adapter, error) {
	s, err := discordgo.New("Bot " + strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent
	a := &Adapter{session: s, platform: "discord", typing: make(map[string]*adapter.TypingState)}
	a.typingFire = func(threadID string) { _ = a.session.ChannelTyping(threadID) }
	for _, o := range opts {
		o(a)
	}
	return a, nil
}

func (a *Adapter) Platform() string { return a.platform }
func (a *Adapter) Healthy() bool    { return a.healthy.Load() }

func (a *Adapter) Start(ctx context.Context) (<-chan adapter.Event, error) {
	a.events = make(chan adapter.Event, 16)

	a.session.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		a.userID = r.User.ID
		a.healthy.Store(true)
		a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
			Platform: a.Platform(), Kind: adapter.LifecycleConnected, At: time.Now(),
		}})
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		// discordgo auto-reconnects under us, so a transient drop is "reconnecting",
		// not a terminal disconnect — emitting Disconnected here made the status
		// panel flap. A successful resume re-emits Connected via the Resumed handler
		// below (Ready only fires on a fresh identify, not a resume).
		a.healthy.Store(false)
		a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
			Platform: a.Platform(), Kind: adapter.LifecycleReconnecting, At: time.Now(),
		}})
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Resumed) {
		a.healthy.Store(true)
		a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
			Platform: a.Platform(), Kind: adapter.LifecycleConnected, At: time.Now(),
		}})
	})
	a.session.AddHandler(a.onMessage)

	if err := a.session.Open(); err != nil {
		return nil, fmt.Errorf("discord: open: %w", err)
	}
	go func() {
		<-ctx.Done()
		_ = a.session.Close()
		close(a.events)
	}()
	return a.events, nil
}

// emitBudget is the max time emit will wait to push an event onto the
// inbound channel. Per docs/specs/adapter.dog.md: "If full for >1s, the adapter
// logs at warn and drops." Package-level so tests can shrink it.
var emitBudget = time.Second

func (a *Adapter) emit(ev adapter.Event) {
	select {
	case a.events <- ev:
		return
	case <-time.After(emitBudget):
		// Channel backpressure. Per docs/specs/adapter.dog.md, repeated drops
		// must emit a Disconnected{cause="downstream backpressure"} so the
		// operator sees something in the web UI status panel. The transport
		// stays up; this is a signal to fix the downstream, not the adapter.
	}
	// Best-effort enqueue of the lifecycle event with a tiny budget — if
	// even that doesn't fit, we genuinely have nowhere to surface it.
	dropEv := adapter.Event{Lifecycle: &adapter.LifecycleEvent{
		Platform: a.Platform(),
		Kind:     adapter.LifecycleDisconnected,
		Cause:    "downstream backpressure",
		At:       time.Now(),
	}}
	select {
	case a.events <- dropEv:
	case <-time.After(emitBudget):
		// Channel is fully wedged; nowhere to put the signal.
	}
}

func (a *Adapter) onMessage(_ *discordgo.Session, m *discordgo.MessageCreate) {
	// discordgo dispatches handlers in their own goroutines and does not recover
	// panics, so a panic in normalizeBody/emit would crash the whole process.
	// Contain it to this one message (it is dropped; the transport stays up).
	defer func() { _ = recover() }()
	if m.Author == nil || m.Author.Bot {
		return // drop our own echoes and any other bot's messages
	}
	if m.Author.ID == a.userID {
		return
	}
	platformThread := m.ChannelID
	body, mentioned := normalizeBody(m.Message, a.userID)
	// DM counts as implicit mention.
	if m.GuildID == "" {
		mentioned = true
	}

	a.emit(adapter.Event{Message: &adapter.MessageEvent{
		Platform:          a.Platform(),
		ThreadID:          platformThread,
		PlatformMessageID: m.ID,
		Author:            adapter.Author{ID: m.Author.ID, Label: m.Author.Username},
		Body:              body,
		Mention:           mentioned,
		ReceivedAt:        time.Now(),
	}})
}

var mentionRE = regexp.MustCompile(`<@!?(\d+)>`)

// normalizeBody strips the bot's mention token from the message body, renders
// attachments to placeholder text, and reports whether the bot was mentioned.
// Exported semantics live in docs/specs/adapter.dog.md "Inbound normalizer".
func normalizeBody(m *discordgo.Message, botUserID string) (string, bool) {
	body := m.Content
	mentioned := false
	for _, u := range m.Mentions {
		if u.ID == botUserID {
			mentioned = true
		}
	}
	body = mentionRE.ReplaceAllStringFunc(body, func(s string) string {
		match := mentionRE.FindStringSubmatch(s)
		if len(match) > 1 && match[1] == botUserID {
			return ""
		}
		return s
	})
	for _, att := range m.Attachments {
		typ := "[attachment]"
		if strings.HasPrefix(att.ContentType, "image/") {
			typ = "[image]"
		}
		if body != "" {
			body += " "
		}
		body += typ
	}
	return strings.TrimSpace(body), mentioned
}

// postBackoff is the per-attempt wait schedule for transient outbound failures.
// Spec: adapter.dog.md "Outbound" — bounded retry so a single transient send
// error doesn't drop the whole reply. (discordgo handles HTTP 429/Retry-After
// internally via its rate limiter, so 429 rarely surfaces as an error here.)
// A var so tests can shrink it.
var postBackoff = []time.Duration{1 * time.Second, 3 * time.Second, 9 * time.Second}

// typingRefresh is how often we re-trigger Discord's typing indicator, which the
// API clears after ~10s. A var so tests can shrink it.
var typingRefresh = 8 * time.Second

// StartTyping implements adapter.Typer. It fires Discord's typing indicator on
// threadID immediately and refreshes it every typingRefresh until the returned
// stop func is called (or ctx is cancelled), pausing while a Post to the same
// thread is in flight (the delivered message clears the bubble on its own).
// Discord has no explicit "stop" call — cancelling lets the indicator lapse.
func (a *Adapter) StartTyping(ctx context.Context, threadID string) func() {
	st := adapter.NewTypingState()
	a.mu.Lock()
	a.typing[threadID] = st
	a.mu.Unlock()

	tctx, cancel := context.WithCancel(ctx)
	go adapter.RunTypingLoop(tctx, st, typingRefresh,
		func() { a.typingFire(threadID) },
		nil)

	return func() {
		cancel()
		a.mu.Lock()
		delete(a.typing, threadID)
		a.mu.Unlock()
	}
}

// Post implements the outbound side. The full body is split into the minimum
// number of <=MaxChunk chunks; each chunk is posted sequentially. Returns the
// platform-native ID of the first chunk for transcript correlation.
func (a *Adapter) Post(ctx context.Context, threadID, body string) (string, error) {
	// Pause any active typing indicator for this thread while we deliver: the
	// posted message replaces the bubble, and EndPost re-fires typing afterward.
	a.mu.Lock()
	st := a.typing[threadID]
	a.mu.Unlock()
	if st != nil {
		st.BeginPost()
		defer st.EndPost()
	}

	chunks := chunk(body, MaxChunk)
	if len(chunks) == 0 {
		return "", nil
	}
	var firstID string
	for i, ch := range chunks {
		msg, err := a.sendWithRetry(ctx, threadID, ch)
		if err != nil {
			if isThreadGone(err) {
				return firstID, adapter.ErrThreadGone
			}
			return firstID, err
		}
		if i == 0 {
			firstID = msg.ID
		}
	}
	return firstID, nil
}

// sendWithRetry posts one chunk, retrying transient failures with bounded
// backoff. A thread-gone error is terminal (returned immediately, no retry).
func (a *Adapter) sendWithRetry(ctx context.Context, threadID, text string) (*discordgo.Message, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		msg, err := a.session.ChannelMessageSend(threadID, text)
		if err == nil {
			return msg, nil
		}
		if isThreadGone(err) {
			return nil, err // terminal; the caller maps it to ErrThreadGone
		}
		lastErr = err
		if attempt >= len(postBackoff) {
			return nil, lastErr
		}
		select {
		case <-time.After(postBackoff[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func isThreadGone(err error) bool {
	if err == nil {
		return false
	}
	if rErr, ok := err.(*discordgo.RESTError); ok && rErr.Response != nil {
		switch rErr.Response.StatusCode {
		case 403, 404:
			return true
		}
	}
	return false
}
