package discord

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/bwmarrin/discordgo"
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
	a := &Adapter{}
	if a.Platform() != "discord" {
		t.Fatalf("platform=%q", a.Platform())
	}
	if a.Healthy() {
		t.Fatal("unstarted adapter must not be healthy")
	}
}
