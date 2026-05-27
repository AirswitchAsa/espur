package discord

import (
	"testing"
	"time"

	"github.com/punny/espur/internal/adapter"
)

// TestEmit_BackpressureSurfacesDisconnected verifies that when the events
// channel is wedged for longer than the emit budget, the adapter surfaces
// a Disconnected{cause="downstream backpressure"} lifecycle event so the
// operator can see the drop in the web UI. docs/specs/adapter.dog.md.
func TestEmit_BackpressureSurfacesDisconnected(t *testing.T) {
	prev := emitBudget
	emitBudget = 20 * time.Millisecond
	t.Cleanup(func() { emitBudget = prev })

	// Buffer of 2: one slot pre-filled with junk so the user-message push
	// times out, then the *next* slot is free for the Disconnected event.
	a := &Adapter{events: make(chan adapter.Event, 2)}
	a.events <- adapter.Event{Lifecycle: &adapter.LifecycleEvent{Kind: adapter.LifecycleConnected}}
	a.events <- adapter.Event{Lifecycle: &adapter.LifecycleEvent{Kind: adapter.LifecycleConnected}}

	// Spawn a draining goroutine that removes one event AFTER emit times
	// out (so the user-msg slot truly couldn't be filled), making the
	// fallback Disconnected event fit.
	go func() {
		time.Sleep(40 * time.Millisecond)
		<-a.events // free one slot for the Disconnected enqueue
	}()

	a.emit(adapter.Event{Message: &adapter.MessageEvent{Body: "doomed"}})

	// Drain everything that landed in the channel and look for the
	// Disconnected{cause="downstream backpressure"} event.
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-a.events:
			if ev.Lifecycle != nil &&
				ev.Lifecycle.Kind == adapter.LifecycleDisconnected &&
				ev.Lifecycle.Cause == "downstream backpressure" {
				return // success
			}
		case <-deadline:
			t.Fatal("never saw Disconnected{cause=\"downstream backpressure\"}")
		}
	}
}

// TestEmit_HappyPath verifies the normal path: when the channel has room,
// the event lands immediately and no fallback lifecycle event is emitted.
func TestEmit_HappyPath(t *testing.T) {
	a := &Adapter{events: make(chan adapter.Event, 4)}
	want := adapter.Event{Message: &adapter.MessageEvent{Body: "hi"}}
	a.emit(want)
	select {
	case got := <-a.events:
		if got.Message == nil || got.Message.Body != "hi" {
			t.Fatalf("got %+v", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("event never landed")
	}
	// And no spurious lifecycle event.
	select {
	case ev := <-a.events:
		t.Fatalf("unexpected extra event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}
