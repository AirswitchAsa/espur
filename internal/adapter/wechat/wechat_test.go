package wechat

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/eatmoreapple/openwechat"

	"github.com/punny/espur/internal/adapter"
)

func TestNew_RequiresStoragePath(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("empty storage path must error")
	}
	a, err := New("/tmp/espur-wechat-session.json")
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

func TestSetUUIDCallback_Stored(t *testing.T) {
	a, _ := New("/tmp/x.json")
	if a.uuidCallback != nil {
		t.Fatal("callback should start nil")
	}
	a.SetUUIDCallback(func(string) {})
	if a.uuidCallback == nil {
		t.Fatal("callback not stored")
	}
}

func TestDisplayName(t *testing.T) {
	cases := []struct {
		name string
		user *openwechat.User
		want string
	}{
		{"nil", nil, ""},
		{"remark wins", &openwechat.User{RemarkName: "R", NickName: "N", UserName: "U"}, "R"},
		{"nick over username", &openwechat.User{NickName: "N", UserName: "U"}, "N"},
		{"username fallback", &openwechat.User{UserName: "U"}, "U"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := displayName(c.user); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
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

func TestPost_NotStarted(t *testing.T) {
	a := &Adapter{}
	_, err := a.Post(context.Background(), "thread-1", "hi")
	if err == nil || !strings.Contains(err.Error(), "not started") {
		t.Fatalf("want not-started error, got %v", err)
	}
}

func TestNormalizeBody_DMNoMention(t *testing.T) {
	body, mention := normalizeBody("hello there", "espur-bot")
	if mention {
		t.Fatal("no @ token should be no mention at this layer")
	}
	if body != "hello there" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_GroupAtBot(t *testing.T) {
	// WeChat group mentions render as "@<name> <text>" where
	// is a four-per-em space (the open-wechat docs note the trailer).
	in := "@espur-bot what's the weather?"
	body, mention := normalizeBody(in, "espur-bot")
	if !mention {
		t.Fatal("should detect @espur-bot mention")
	}
	if body != "what's the weather?" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_OtherMentionsStripped(t *testing.T) {
	in := "@alice @bob ping the bot please"
	body, mention := normalizeBody(in, "espur-bot")
	if mention {
		t.Fatal("no @espur-bot here")
	}
	if body != "ping the bot please" {
		t.Fatalf("body=%q", body)
	}
}

func TestNormalizeBody_NoBotName(t *testing.T) {
	body, mention := normalizeBody("hi", "")
	if mention {
		t.Fatal("empty botName must not match anything")
	}
	if body != "hi" {
		t.Fatalf("body=%q", body)
	}
}
