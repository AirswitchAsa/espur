// Package discord implements the Discord IM adapter. See specs/adapter.dog.md
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
	session *discordgo.Session
	userID  string

	mu      sync.Mutex
	events  chan adapter.Event
	healthy atomic.Bool
}

// New constructs an unstarted Discord adapter. token must be a bot token
// (decrypted from secrets just before construction; the adapter does not
// look it up itself).
func New(token string) (*Adapter, error) {
	s, err := discordgo.New("Bot " + strings.TrimSpace(token))
	if err != nil {
		return nil, err
	}
	s.Identify.Intents = discordgo.IntentsGuildMessages |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsMessageContent
	return &Adapter{session: s}, nil
}

func (a *Adapter) Platform() string { return "discord" }
func (a *Adapter) Healthy() bool    { return a.healthy.Load() }

func (a *Adapter) Start(ctx context.Context) (<-chan adapter.Event, error) {
	a.events = make(chan adapter.Event, 16)

	a.session.AddHandler(func(_ *discordgo.Session, r *discordgo.Ready) {
		a.userID = r.User.ID
		a.healthy.Store(true)
		a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
			Platform: "discord", Kind: adapter.LifecycleConnected, At: time.Now(),
		}})
	})
	a.session.AddHandler(func(_ *discordgo.Session, _ *discordgo.Disconnect) {
		a.healthy.Store(false)
		a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
			Platform: "discord", Kind: adapter.LifecycleDisconnected, At: time.Now(),
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
// inbound channel. Per specs/adapter.dog.md: "If full for >1s, the adapter
// logs at warn and drops." Package-level so tests can shrink it.
var emitBudget = time.Second

func (a *Adapter) emit(ev adapter.Event) {
	select {
	case a.events <- ev:
		return
	case <-time.After(emitBudget):
		// Channel backpressure. Per specs/adapter.dog.md, repeated drops
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
		Platform:          "discord",
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
// Exported semantics live in specs/adapter.dog.md "Inbound normalizer".
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

// Post implements the outbound side. The full body is split into the minimum
// number of <=MaxChunk chunks; each chunk is posted sequentially. Returns the
// platform-native ID of the first chunk for transcript correlation.
func (a *Adapter) Post(ctx context.Context, threadID, body string) (string, error) {
	chunks := chunk(body, MaxChunk)
	if len(chunks) == 0 {
		return "", nil
	}
	var firstID string
	for i, ch := range chunks {
		msg, err := a.session.ChannelMessageSend(threadID, ch)
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
