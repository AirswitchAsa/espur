package wechat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ilink "github.com/openilink/openilink-sdk-go"

	"github.com/punny/espur/internal/adapter"
)

// fakeClient is a network-free stand-in for the iLink SDK client.
type fakeClient struct {
	sendTextCalls []sendTextCall
	sendErr       error
	baseURL       string
}

type sendTextCall struct {
	to, text, token string
}

func (f *fakeClient) LoginWithQR(_ context.Context, _ *ilink.LoginCallbacks) (*ilink.LoginResult, error) {
	return &ilink.LoginResult{Connected: true, BotToken: "tok", BotID: "bot", UserID: "self"}, nil
}

func (f *fakeClient) Monitor(ctx context.Context, _ ilink.MessageHandler, _ *ilink.MonitorOptions) error {
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeClient) SendText(_ context.Context, to, text, token string) (string, error) {
	f.sendTextCalls = append(f.sendTextCalls, sendTextCall{to, text, token})
	if f.sendErr != nil {
		return "", f.sendErr
	}
	return "msgid-" + to + "-" + token, nil
}

func (f *fakeClient) SetBaseURL(url string) { f.baseURL = url }

func TestNew_RequiresStore(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("nil session store must error")
	}
	a, err := New(NewFileStore("/tmp/espur-wechat-session.json"))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.Platform() != "wechat" {
		t.Fatalf("platform=%q", a.Platform())
	}
	if a.Healthy() {
		t.Fatal("a freshly-constructed adapter must not report healthy")
	}
}

func TestNew_AppliesOptions(t *testing.T) {
	a, err := New(NewFileStore("/tmp/x.json"), WithBaseURL("https://example.test"))
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if a.baseURL != "https://example.test" {
		t.Fatalf("baseURL=%q", a.baseURL)
	}
}

func TestWithPlatformKey(t *testing.T) {
	a, err := New(NewFileStore("/tmp/x.json"), WithPlatformKey("wechat:abc123"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if a.Platform() != "wechat:abc123" {
		t.Fatalf("platform=%q want wechat:abc123", a.Platform())
	}
	// Default and empty-key behaviour.
	b, _ := New(NewFileStore("/tmp/x.json"))
	if b.Platform() != "wechat" {
		t.Fatalf("default platform=%q want wechat", b.Platform())
	}
	c, _ := New(NewFileStore("/tmp/x.json"), WithPlatformKey("  "))
	if c.Platform() != "wechat" {
		t.Fatalf("empty key should keep default, got %q", c.Platform())
	}
}

func TestSetQRCallback_Stored(t *testing.T) {
	a, _ := New(NewFileStore("/tmp/x.json"))
	if a.qrCallback != nil {
		t.Fatal("callback should start nil")
	}
	a.SetQRCallback(func(string) {})
	if a.qrCallback == nil {
		t.Fatal("callback not stored")
	}
}

func TestNormalizeBody_PlainText(t *testing.T) {
	if got := normalizeBody("hello there"); got != "hello there" {
		t.Fatalf("body=%q", got)
	}
}

func TestNormalizeBody_StripsMentionTokens(t *testing.T) {
	if got := normalizeBody("@alice @bob ping the bot please"); got != "ping the bot please" {
		t.Fatalf("body=%q", got)
	}
}

func TestNormalizeMessage_MediaPlaceholder(t *testing.T) {
	msg := &ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemImage, ImageItem: &ilink.ImageItem{}},
		},
	}
	if got := normalizeMessage(msg); got != "[image]" {
		t.Fatalf("body=%q want [image]", got)
	}
}

func TestNormalizeMessage_TextWins(t *testing.T) {
	msg := &ilink.WeixinMessage{
		ItemList: []ilink.MessageItem{
			{Type: ilink.ItemText, TextItem: &ilink.TextItem{Text: "hi @espur-bot"}},
		},
	}
	if got := normalizeMessage(msg); got != "hi" {
		t.Fatalf("body=%q", got)
	}
}

func TestEmit_DeliversEvent(t *testing.T) {
	a := &Adapter{events: make(chan adapter.Event, 1)}
	a.emit(adapter.Event{Lifecycle: &adapter.LifecycleEvent{Kind: adapter.LifecycleConnected}})
	select {
	case ev := <-a.events:
		if ev.Lifecycle == nil || ev.Lifecycle.Kind != adapter.LifecycleConnected {
			t.Fatalf("got %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("event never landed")
	}
}

func TestOnMessage_SelfEchoFiltered(t *testing.T) {
	// The login UserID is the human OWNER's id (an "@im.wechat" id), not the
	// bot's own "@im.bot" id — so it must NOT be used to filter inbound. Only
	// MsgTypeBot (the bot's own echoes) is dropped.
	a := &Adapter{events: make(chan adapter.Event, 4), threads: map[string]string{}}

	// Bot-typed message (our own outbound, redelivered) must be dropped.
	a.onMessage(ilink.WeixinMessage{
		FromUserID:  "peer",
		MessageType: ilink.MsgTypeBot,
		ItemList:    []ilink.MessageItem{{Type: ilink.ItemText, TextItem: &ilink.TextItem{Text: "echo"}}},
	})
	select {
	case ev := <-a.events:
		t.Fatalf("MsgTypeBot must be dropped, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	// A normal user message sent by the OWNER's own account is legitimate
	// inbound (the operator talking to their bot) and must be delivered.
	a.onMessage(ilink.WeixinMessage{
		FromUserID:   "owner",
		MessageType:  ilink.MsgTypeUser,
		ContextToken: "ctx",
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemText, TextItem: &ilink.TextItem{Text: "hi from owner"}}},
	})
	select {
	case ev := <-a.events:
		if ev.Message == nil || ev.Message.Author.ID != "owner" {
			t.Fatalf("owner's own message should be delivered, got %+v", ev)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("owner's user message was wrongly dropped")
	}
}

func TestOnMessage_GroupDropped(t *testing.T) {
	// Groups are unsupported; an inbound group message must be dropped and
	// must not cache a thread token.
	a := &Adapter{events: make(chan adapter.Event, 4), threads: map[string]string{}}
	a.onMessage(ilink.WeixinMessage{
		FromUserID:   "alice",
		GroupID:      "grp-1",
		MessageType:  ilink.MsgTypeUser,
		ContextToken: "ctx-g",
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemText, TextItem: &ilink.TextItem{Text: "@espur-bot hi"}}},
	})
	select {
	case ev := <-a.events:
		t.Fatalf("group message must be dropped, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
	if _, ok := a.threads["grp-1"]; ok {
		t.Fatal("group message must not cache a thread token")
	}
}

func TestOnMessage_DMEmitsAndCachesToken(t *testing.T) {
	a := &Adapter{events: make(chan adapter.Event, 4), threads: map[string]string{}}
	a.onMessage(ilink.WeixinMessage{
		FromUserID:   "peer",
		MessageID:    42,
		MessageType:  ilink.MsgTypeUser,
		ContextToken: "ctx-1",
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemText, TextItem: &ilink.TextItem{Text: "hello"}}},
	})

	select {
	case ev := <-a.events:
		m := ev.Message
		if m == nil {
			t.Fatalf("expected message event, got %+v", ev)
		}
		if m.ThreadID != "peer" {
			t.Fatalf("threadID=%q want peer", m.ThreadID)
		}
		if m.PlatformMessageID != "42" {
			t.Fatalf("msgID=%q want 42", m.PlatformMessageID)
		}
		if !m.Mention {
			t.Fatal("DM must be an implicit mention")
		}
		if m.Body != "hello" {
			t.Fatalf("body=%q", m.Body)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("no event")
	}

	if token, ok := a.threads["peer"]; !ok || token != "ctx-1" {
		t.Fatalf("token not cached: %q ok=%v", token, ok)
	}
}

func TestPost_NotStarted(t *testing.T) {
	a := &Adapter{threads: map[string]string{}}
	_, err := a.Post(context.Background(), "thread-1", "hi")
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("want not-started error, got %v", err)
	}
}

func TestPost_UnknownThread_ErrThreadGone(t *testing.T) {
	a := &Adapter{client: &fakeClient{}, threads: map[string]string{}}
	_, err := a.Post(context.Background(), "never-messaged", "hi")
	if err != adapter.ErrThreadGone {
		t.Fatalf("want ErrThreadGone, got %v", err)
	}
}

func TestPost_EmptyBody(t *testing.T) {
	a := &Adapter{client: &fakeClient{}, threads: map[string]string{"peer": "ctx"}}
	id, err := a.Post(context.Background(), "peer", "")
	if err != nil || id != "" {
		t.Fatalf("empty body: id=%q err=%v", id, err)
	}
}

func TestPost_DMChunksAndReturnsFirstID(t *testing.T) {
	f := &fakeClient{}
	a := &Adapter{client: f, threads: map[string]string{"peer": "ctx-1"}}

	// A body longer than MaxChunk forces textchunk.Split into >1 chunk.
	body := strings.Repeat("a", MaxChunk+200)
	id, err := a.Post(context.Background(), "peer", body)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(f.sendTextCalls) != 2 {
		t.Fatalf("expected 2 chunked sends, got %d", len(f.sendTextCalls))
	}
	if f.sendTextCalls[0].token != "ctx-1" {
		t.Fatalf("send did not echo cached token: %q", f.sendTextCalls[0].token)
	}
	if id != "msgid-peer-ctx-1" {
		t.Fatalf("firstID=%q (should be first chunk's id)", id)
	}
}

func TestSession_RoundtripAndCursorPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wechat-session.json")
	a, err := New(NewFileStore(path))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Missing file -> zero session, no error.
	if s := a.loadSession(); s != (session{}) {
		t.Fatalf("missing file should load zero session, got %+v", s)
	}

	want := session{BotToken: "tok", BotID: "bot", UserID: "u", BaseURL: "https://x", Cursor: "cur-0"}
	if err := a.saveSession(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	if got := a.loadSession(); got != want {
		t.Fatalf("roundtrip mismatch: got %+v want %+v", got, want)
	}

	// saveCursor (the OnBufUpdate hook) advances and persists the cursor.
	a.mu.Lock()
	a.sess = want
	a.mu.Unlock()
	a.saveCursor("cur-1")
	if got := a.loadSession(); got.Cursor != "cur-1" {
		t.Fatalf("cursor not persisted: got %q", got.Cursor)
	}
}

func TestClearAuth_WipesPersistedToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wechat-session.json")
	a, _ := New(NewFileStore(path))
	_ = a.saveSession(session{BotToken: "tok", Cursor: "cur"})
	a.clearAuth()
	if got := a.loadSession(); got != (session{}) {
		t.Fatalf("clearAuth should empty the session, got %+v", got)
	}
}
