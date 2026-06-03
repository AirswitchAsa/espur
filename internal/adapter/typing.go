package adapter

import (
	"context"
	"sync/atomic"
	"time"
)

// Typer is an optional capability an Adapter may implement: showing a "typing…"
// indicator on a thread for the duration of an agent run. The core calls
// StartTyping when it begins processing a triggering message and calls the
// returned stop func when the reply is done (via defer), so the indicator spans
// the whole run and clears on completion.
//
// Contract:
//   - StartTyping MUST return a non-nil stop func and MUST NOT block on network
//     I/O — do the first trigger and any handshake inside a goroutine so the
//     caller proceeds immediately.
//   - Typing is best-effort. Implementations swallow transient errors (the
//     indicator simply lapses) and never affect the reply path.
//   - The adapter is responsible for pausing the indicator while its own Post is
//     in flight (the delivered message replaces the bubble); TypingState +
//     RunTypingLoop below implement that coordination.
type Typer interface {
	StartTyping(ctx context.Context, threadID string) (stop func())
}

// TypingState coordinates a keepalive loop with in-flight Posts so the indicator
// pauses while a message is being delivered and resumes promptly afterward. It
// is shared by every adapter that implements Typer. Construct with
// NewTypingState; the zero value is not usable.
//
// Concurrency: BeginPost/EndPost are called from the goroutine running Post; the
// keepalive loop runs on its own goroutine. The per-thread work queue serializes
// Posts for a given thread, so the in-flight counter only ever toggles 0↔1 in
// practice, but the atomic makes nested/overlapping calls safe regardless.
type TypingState struct {
	posting atomic.Int32
	resume  chan struct{} // buffered(1): nudge the loop to re-fire after a post
}

// NewTypingState returns a ready TypingState.
func NewTypingState() *TypingState {
	return &TypingState{resume: make(chan struct{}, 1)}
}

// BeginPost marks a Post as in flight; RunTypingLoop suppresses the indicator
// until the matching EndPost.
func (s *TypingState) BeginPost() { s.posting.Add(1) }

// EndPost clears one in-flight Post. When the last one completes it nudges the
// loop to re-fire immediately, so typing reappears in the next thinking gap
// without waiting out the refresh interval.
func (s *TypingState) EndPost() {
	if s.posting.Add(-1) == 0 {
		select {
		case s.resume <- struct{}{}:
		default: // a nudge is already pending; one is enough
		}
	}
}

// Idle reports whether no Post is currently in flight.
func (s *TypingState) Idle() bool { return s.posting.Load() == 0 }

// RunTypingLoop drives a typing indicator until ctx is done. It calls fire once
// immediately and again on every refresh tick, but never while a Post is in
// flight (see TypingState); when a Post completes it re-fires promptly. On ctx
// cancellation it calls onStop (may be nil) for a final explicit clear.
//
// fire and onStop are the platform triggers; both may perform best-effort
// network I/O and should ignore their own errors.
func RunTypingLoop(ctx context.Context, s *TypingState, refresh time.Duration, fire func(), onStop func()) {
	if s.Idle() {
		fire()
	}
	t := time.NewTicker(refresh)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if onStop != nil {
				onStop()
			}
			return
		case <-t.C:
			if s.Idle() {
				fire()
			}
		case <-s.resume:
			if s.Idle() {
				fire()
				t.Reset(refresh)
			}
		}
	}
}
