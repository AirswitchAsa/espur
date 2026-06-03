package discord

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"

	"github.com/punny/espur/internal/adapter"
)

func TestIsThreadGone(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"403 forbidden", &discordgo.RESTError{Response: &http.Response{StatusCode: 403}}, true},
		{"404 not found", &discordgo.RESTError{Response: &http.Response{StatusCode: 404}}, true},
		{"500 server", &discordgo.RESTError{Response: &http.Response{StatusCode: 500}}, false},
		{"rest error no response", &discordgo.RESTError{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isThreadGone(c.err); got != c.want {
				t.Fatalf("isThreadGone(%v)=%v want %v", c.err, got, c.want)
			}
		})
	}
}

// TestPost_EmptyBody verifies the empty-body short-circuit returns before
// touching the (nil) discord session, so no chunk is ever sent.
func TestPost_EmptyBody(t *testing.T) {
	a := &Adapter{}
	id, err := a.Post(context.Background(), "thread-1", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Fatalf("expected empty id, got %q", id)
	}
}

func TestPlatformAndHealthy(t *testing.T) {
	a, err := New("token-x")
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if a.Platform() != "discord" {
		t.Fatalf("default platform=%q want discord", a.Platform())
	}
	if a.Healthy() {
		t.Fatal("unstarted adapter must not be healthy")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("condition not met within deadline")
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// TestStartTyping_FiresPausesAndStops drives the indicator through a substituted
// fire func (no live session): it fires immediately, suppresses fires while a
// Post is in flight, re-fires when the Post completes, and removes the per-thread
// state on stop.
func TestStartTyping_FiresPausesAndStops(t *testing.T) {
	orig := typingRefresh
	typingRefresh = 10 * time.Millisecond
	defer func() { typingRefresh = orig }()

	a := &Adapter{typing: map[string]*adapter.TypingState{}}
	var fires int32
	a.typingFire = func(string) { atomic.AddInt32(&fires, 1) }

	stop := a.StartTyping(context.Background(), "ch")
	waitFor(t, func() bool { return atomic.LoadInt32(&fires) >= 1 }) // initial fire

	a.mu.Lock()
	st := a.typing["ch"]
	a.mu.Unlock()
	if st == nil {
		t.Fatal("StartTyping should register a typing state for the thread")
	}

	// While a Post is in flight, the indicator must not refresh.
	st.BeginPost()
	base := atomic.LoadInt32(&fires)
	time.Sleep(50 * time.Millisecond) // several refresh intervals
	if got := atomic.LoadInt32(&fires); got != base {
		t.Fatalf("typing fired %d times while a Post was in flight (want %d)", got, base)
	}

	// Completing the Post re-fires promptly.
	st.EndPost()
	waitFor(t, func() bool { return atomic.LoadInt32(&fires) > base })

	stop()
	a.mu.Lock()
	_, tracked := a.typing["ch"]
	a.mu.Unlock()
	if tracked {
		t.Fatal("stop() should remove the typing state")
	}
}

// TestPost_PausesTyping verifies Post toggles the typing state (BeginPost/EndPost
// balanced) using the empty-body short-circuit so no session call is made.
func TestPost_PausesTyping(t *testing.T) {
	a := &Adapter{typing: map[string]*adapter.TypingState{}}
	st := adapter.NewTypingState()
	a.typing["ch"] = st

	if _, err := a.Post(context.Background(), "ch", ""); err != nil {
		t.Fatalf("post: %v", err)
	}
	if !st.Idle() {
		t.Fatal("Post should leave the typing state idle (BeginPost/EndPost balanced)")
	}
}

func TestWithPlatformKey(t *testing.T) {
	a, err := New("token-x", WithPlatformKey("discord:abc123"))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if a.Platform() != "discord:abc123" {
		t.Fatalf("platform=%q want discord:abc123", a.Platform())
	}
	// Empty key must not clobber the default.
	b, _ := New("token-x", WithPlatformKey("  "))
	if b.Platform() != "discord" {
		t.Fatalf("empty key should keep default, got %q", b.Platform())
	}
}
