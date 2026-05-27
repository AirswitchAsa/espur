// Package wechat implements the WeChat IM adapter via github.com/eatmoreapple/openwechat.
// This is a *personal* WeChat session: it logs in by QR scan from a phone and
// runs against the web/desktop WeChat protocol. Tencent has been known to
// flag accounts that automate; the espur deploy doc warns operators. See
// docs/specs/adapter.dog.md for the abstract contract this satisfies.
package wechat

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eatmoreapple/openwechat"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/adapter/textchunk"
)

// MaxChunk is the safe length WeChat web-protocol accepts in one text send.
// The wire protocol allows more, but messages near the cap are sometimes
// truncated server-side; 1800 leaves headroom. Mirror Discord's hard-split
// behaviour for code-fenced output.
const MaxChunk = 1800

// Adapter implements adapter.Adapter for personal WeChat.
type Adapter struct {
	storagePath string

	mu      sync.Mutex
	bot     *openwechat.Bot
	self    *openwechat.Self
	events  chan adapter.Event
	healthy atomic.Bool
	// botName is the display name we strip from inbound bodies to expose
	// "@bot" as an explicit mention marker. Captured at login.
	botName string

	// uuidCallback is exposed for tests and to allow the operator to see
	// the QR URL via the structured log surface. nil → default (log).
	uuidCallback func(uuid string)
}

// New constructs an unstarted WeChat adapter. storagePath is the file path
// at which openwechat persists the post-login hot-reload session, so that
// subsequent boots don't require a fresh QR scan. The directory MUST exist.
func New(storagePath string) (*Adapter, error) {
	if storagePath == "" {
		return nil, errors.New("wechat: storage path is required")
	}
	return &Adapter{storagePath: storagePath}, nil
}

func (a *Adapter) Platform() string { return "wechat" }
func (a *Adapter) Healthy() bool    { return a.healthy.Load() }

// SetUUIDCallback overrides what happens when openwechat emits the login
// UUID. Default is to log it at info; tests can capture it.
func (a *Adapter) SetUUIDCallback(fn func(uuid string)) { a.uuidCallback = fn }

// Start opens the WeChat session, registers a message handler, and returns
// an event channel like every other adapter. Login flow:
//
//  1. If the hot-reload storage file is present and still valid, login is
//     transparent (no QR).
//  2. Otherwise openwechat emits the login UUID; the operator scans the QR
//     code at https://login.weixin.qq.com/qrcode/<uuid> from the WeChat
//     mobile client.
//
// Either way, after a successful login the hot-reload file is rewritten so
// the next boot can skip QR.
func (a *Adapter) Start(ctx context.Context) (<-chan adapter.Event, error) {
	a.events = make(chan adapter.Event, 16)

	a.bot = openwechat.DefaultBot(openwechat.Desktop)
	a.bot.UUIDCallback = func(uuid string) {
		if a.uuidCallback != nil {
			a.uuidCallback(uuid)
			return
		}
		// Default: surface the QR URL for the operator (no message body,
		// no credential — just the URL).
		fmt.Printf("{\"event\":\"wechat.login.uuid\",\"qr_url\":\"https://login.weixin.qq.com/qrcode/%s\"}\n", uuid)
	}
	a.bot.MessageHandler = a.onMessage
	a.bot.LogoutCallBack = func(_ *openwechat.Bot) {
		a.healthy.Store(false)
		a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
			Platform: a.Platform(), Kind: adapter.LifecycleDisconnected, At: time.Now(),
		}})
	}

	storage := openwechat.NewFileHotReloadStorage(a.storagePath)
	if err := a.bot.HotLogin(storage, openwechat.NewRetryLoginOption()); err != nil {
		// HotLogin falls back to a QR scan automatically. An error here means
		// even the QR path failed — usually because the host can't reach
		// login.weixin.qq.com or the scan was abandoned.
		return nil, fmt.Errorf("wechat: login: %w", err)
	}

	self, err := a.bot.GetCurrentUser()
	if err != nil {
		return nil, fmt.Errorf("wechat: get self: %w", err)
	}
	a.self = self
	a.botName = strings.TrimSpace(self.NickName)
	a.healthy.Store(true)
	a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{
		Platform: a.Platform(), Kind: adapter.LifecycleConnected, At: time.Now(),
	}})

	// openwechat runs its message loop in a goroutine internally; we just
	// have to stop the bot on ctx cancellation and close the events chan.
	go func() {
		<-ctx.Done()
		a.bot.Exit()
		close(a.events)
	}()

	return a.events, nil
}

func (a *Adapter) emit(ev adapter.Event) {
	select {
	case a.events <- ev:
	case <-time.After(time.Second):
		// Backpressure: drop. Lifecycle events are best-effort.
	}
}

// onMessage runs on openwechat's loop goroutine. We translate the message
// into the abstract event and emit. System messages (joins, tickles, sync
// notifications) are dropped silently — they are not user-actionable in the
// chat-bot frame.
func (a *Adapter) onMessage(m *openwechat.Message) {
	if m == nil || !m.IsText() {
		return
	}
	if m.IsSendBySelf() {
		return // ignore echoes of our own outbound posts
	}

	sender, err := m.Sender()
	if err != nil {
		return
	}
	threadID := m.FromUserName
	// Author label: prefer remark/nick; fall back to userName.
	var label, authorID string
	if m.IsSendByGroup() {
		// In groups, FromUserName is the group; the actual sender is in
		// the message body as a "<senderUserName>:\n<text>" prefix that
		// openwechat normalises into SenderInGroup.
		if inGroup, ierr := m.SenderInGroup(); ierr == nil && inGroup != nil {
			label = displayName(inGroup)
			authorID = inGroup.UserName
		} else {
			label = displayName(sender)
			authorID = sender.UserName
		}
	} else {
		label = displayName(sender)
		authorID = sender.UserName
	}

	body, mentioned := normalizeBody(m.Content, a.botName)
	if !m.IsSendByGroup() {
		mentioned = true // DM implicit mention, like Discord
	} else if m.IsAt() {
		mentioned = true
	}

	a.emit(adapter.Event{Message: &adapter.MessageEvent{
		Platform:          a.Platform(),
		ThreadID:          threadID,
		PlatformMessageID: m.MsgId,
		Author:            adapter.Author{ID: authorID, Label: label},
		Body:              body,
		Mention:           mentioned,
		ReceivedAt:        time.Now(),
	}})
}

func displayName(u *openwechat.User) string {
	if u == nil {
		return ""
	}
	if u.RemarkName != "" {
		return u.RemarkName
	}
	if u.NickName != "" {
		return u.NickName
	}
	return u.UserName
}

// mentionRE matches the "@nickname " pattern WeChat uses to render
// mentions in group chats (the trailing char is a four-per-em space).
var mentionTrailer = regexp.MustCompile(`@([^\s\x{2005}]+)\x{2005}?`)

// normalizeBody strips an "@<botname>" mention token from the body and
// reports whether the bot was mentioned. WeChat doesn't expose mentions as
// a structured array, only the text — so we match on the bot's display
// name. If the operator renamed the bot post-login this will miss; live
// with it for v0.
func normalizeBody(content, botName string) (string, bool) {
	mentioned := false
	if botName != "" {
		needle := "@" + botName
		if strings.Contains(content, needle) {
			mentioned = true
		}
	}
	// Strip any "@xxx" mention tokens — they're ornamentation, not part of
	// the user's intent for the bot.
	cleaned := mentionTrailer.ReplaceAllString(content, "")
	return strings.TrimSpace(cleaned), mentioned
}

// Post sends body to the WeChat user/group identified by threadID. Splits
// into MaxChunk-sized chunks. Returns the first chunk's MsgId for
// transcript correlation.
func (a *Adapter) Post(ctx context.Context, threadID, body string) (string, error) {
	a.mu.Lock()
	self := a.self
	a.mu.Unlock()
	if self == nil {
		return "", errors.New("wechat: not started")
	}

	members, err := self.Members()
	if err != nil {
		return "", fmt.Errorf("wechat: members: %w", err)
	}
	target, ok := members.GetByUserName(threadID)
	if !ok {
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
		var sent *openwechat.SentMessage
		var serr error
		switch {
		case target.IsGroup():
			sent, serr = self.SendTextToGroup(&openwechat.Group{User: target}, ch)
		case target.IsFriend():
			sent, serr = self.SendTextToFriend(&openwechat.Friend{User: target}, ch)
		default:
			// MP or unknown — try as friend; openwechat will reject if not
			// addressable, which we surface as a real error.
			sent, serr = self.SendTextToFriend(&openwechat.Friend{User: target}, ch)
		}
		if serr != nil {
			return firstID, serr
		}
		if i == 0 && sent != nil {
			firstID = sent.MsgId
		}
	}
	return firstID, nil
}
