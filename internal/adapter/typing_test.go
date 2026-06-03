package adapter

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// waitFor polls cond until true or the deadline elapses.
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

// TestRunTypingLoop_FiresInitiallyAndOnStop verifies the loop triggers once
// immediately and calls onStop exactly once when ctx is cancelled.
func TestRunTypingLoop_FiresInitiallyAndOnStop(t *testing.T) {
	st := NewTypingState()
	var fires, stops int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunTypingLoop(ctx, st, time.Hour, // long refresh: no ticks during the test
			func() { atomic.AddInt32(&fires, 1) },
			func() { atomic.AddInt32(&stops, 1) })
		close(done)
	}()

	waitFor(t, func() bool { return atomic.LoadInt32(&fires) == 1 })
	cancel()
	<-done
	if got := atomic.LoadInt32(&fires); got != 1 {
		t.Fatalf("fires=%d want 1 (no tick should occur with an hour refresh)", got)
	}
	if got := atomic.LoadInt32(&stops); got != 1 {
		t.Fatalf("stops=%d want 1", got)
	}
}

// TestRunTypingLoop_PausesDuringPostAndResumes verifies a Post in flight
// suppresses the indicator and that completing the last Post re-fires it
// promptly via the resume nudge (no dependence on the refresh tick).
func TestRunTypingLoop_PausesDuringPostAndResumes(t *testing.T) {
	st := NewTypingState()
	var fires int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunTypingLoop(ctx, st, time.Hour, // long refresh: re-fire must come from resume, not a tick
		func() { atomic.AddInt32(&fires, 1) }, nil)

	waitFor(t, func() bool { return atomic.LoadInt32(&fires) == 1 }) // initial fire

	st.BeginPost()
	if st.Idle() {
		t.Fatal("Idle() must be false while a Post is in flight")
	}
	st.EndPost() // last post done -> resume nudge -> re-fire
	waitFor(t, func() bool { return atomic.LoadInt32(&fires) == 2 })
}

// TestRunTypingLoop_NestedPostsHoldPause verifies typing only resumes after the
// LAST overlapping Post completes (counter, not boolean).
func TestRunTypingLoop_NestedPostsHoldPause(t *testing.T) {
	st := NewTypingState()
	var fires int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go RunTypingLoop(ctx, st, time.Hour,
		func() { atomic.AddInt32(&fires, 1) }, nil)
	waitFor(t, func() bool { return atomic.LoadInt32(&fires) == 1 })

	st.BeginPost()
	st.BeginPost()
	st.EndPost()
	if st.Idle() {
		t.Fatal("still one Post in flight; must not be idle")
	}
	st.EndPost() // now the last one completes -> re-fire
	waitFor(t, func() bool { return atomic.LoadInt32(&fires) == 2 })
}
