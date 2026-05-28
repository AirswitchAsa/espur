package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/opencode"
)

// blockingInvoker holds Invoke until release is closed, then returns the
// scripted result. Lets us pin a HandleTrigger goroutine in the middle of
// "in-flight" so the shutdown sequencer has something to drain. ctxObserved
// captures whatever context the invocation saw, so tests can assert the
// exec context was (or wasn't) cancelled.
type blockingInvoker struct {
	release      chan struct{}
	ctxObserved  chan context.Context
	finalOutcome opencode.Outcome
}

func (b *blockingInvoker) Invoke(ctx context.Context, _ opencode.Request) (opencode.Result, error) {
	if b.ctxObserved != nil {
		select {
		case b.ctxObserved <- ctx:
		default:
		}
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return opencode.Result{Outcome: opencode.OutcomeCrash, CrashReason: "ctx_cancelled"}, ctx.Err()
	}
	return opencode.Result{Outcome: b.finalOutcome, AssistantText: "ok"}, nil
}

// TestShutdown_StopAcceptingRejectsNewTriggers verifies phase-1 of the
// sequencer: once StopAccepting fires, Dispatch silently drops mention-
// bearing messages. The transcript / dedup side effects are also skipped.
func TestShutdown_StopAcceptingRejectsNewTriggers(t *testing.T) {
	inv := &blockingInvoker{
		release:      make(chan struct{}),
		finalOutcome: opencode.OutcomeSuccess,
	}
	close(inv.release) // never blocks; we just need an Invoker
	core, fa, db := newCore(t, inv)

	core.StopAccepting()
	core.Dispatch(context.Background(), adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-shut-1",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}})

	select {
	case <-fa.done:
		t.Fatal("post-StopAccepting trigger produced a Post")
	case <-time.After(150 * time.Millisecond):
	}
	// And the dedup row must NOT have been written — Dispatch returned
	// before even reaching the dedup table.
	first, err := db.SeenMessage(context.Background(), "discord", "m-shut-1")
	if err != nil {
		t.Fatal(err)
	}
	if !first {
		t.Fatal("dedup row was written despite StopAccepting")
	}
}

// TestShutdown_DrainCountsAcceptedBeforeWorkerStarts is a regression test for
// the drain race: a message that has been accepted (Dispatch has returned) must
// be counted by WaitDrain even if its worker goroutine has not yet begun the
// invocation. Previously the inflight WaitGroup was incremented inside the
// worker, so a shutdown beginning in the gap between "accepted" and "worker
// running" could observe an empty WaitGroup and tear down the DB under a
// starting invocation. We deliberately do NOT wait for the invocation to start.
func TestShutdown_DrainCountsAcceptedBeforeWorkerStarts(t *testing.T) {
	inv := &blockingInvoker{
		release:      make(chan struct{}),
		finalOutcome: opencode.OutcomeSuccess,
	}
	core, _, _ := newCore(t, inv)

	core.Dispatch(context.Background(), adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-race",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}})
	// No sync on invocation start — begin shutdown immediately.
	core.StopAccepting()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if core.WaitDrain(ctx) {
		t.Fatal("WaitDrain returned true while an accepted message was still pending")
	}
	// Drain the worker before returning so its async writes don't race t.TempDir
	// cleanup.
	close(inv.release)
	drainCtx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dcancel()
	core.WaitDrain(drainCtx)
}

// TestShutdown_WaitDrainCompletesOnInflightFinish drives a trigger into
// in-flight state, then asserts WaitDrain blocks until the invocation
// finishes and returns true.
func TestShutdown_WaitDrainCompletesOnInflightFinish(t *testing.T) {
	inv := &blockingInvoker{
		release:      make(chan struct{}),
		ctxObserved:  make(chan context.Context, 1),
		finalOutcome: opencode.OutcomeSuccess,
	}
	core, fa, _ := newCore(t, inv)

	core.Dispatch(context.Background(), adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-inflight",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}})

	// Wait for the invocation to actually start.
	select {
	case <-inv.ctxObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("invocation never started")
	}

	// Phase 1.
	core.StopAccepting()

	drainResult := make(chan bool, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		drainResult <- core.WaitDrain(ctx)
	}()

	// WaitDrain must NOT return yet — invocation still pinned.
	select {
	case <-drainResult:
		t.Fatal("WaitDrain returned before in-flight invocation finished")
	case <-time.After(100 * time.Millisecond):
	}

	close(inv.release)
	select {
	case ok := <-drainResult:
		if !ok {
			t.Fatal("WaitDrain returned false despite in-flight finishing in time")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitDrain did not return after release")
	}
	// And the reply was posted as normal.
	select {
	case <-fa.done:
	case <-time.After(time.Second):
		t.Fatal("expected a Post after drain")
	}
}

// TestShutdown_WaitDrainTimesOut verifies the false-return path: drain
// deadline elapses while the invocation is still pinned.
func TestShutdown_WaitDrainTimesOut(t *testing.T) {
	inv := &blockingInvoker{
		release:      make(chan struct{}),
		ctxObserved:  make(chan context.Context, 1),
		finalOutcome: opencode.OutcomeSuccess,
	}
	core, _, _ := newCore(t, inv)

	core.Dispatch(context.Background(), adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-timeout",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}})
	<-inv.ctxObserved // ensure in-flight

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if ok := core.WaitDrain(ctx); ok {
		t.Fatal("WaitDrain returned true despite deadline expiring")
	}
	// Unblock and then wait for the worker to actually finish before the test
	// returns — otherwise its async transcript writes race with the t.TempDir
	// cleanup ("directory not empty").
	close(inv.release)
	drainCtx, dcancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer dcancel()
	core.WaitDrain(drainCtx)
}

// TestShutdown_AbortInFlightCancelsExecContext verifies that the second-
// signal escalation path actually cancels the exec context the Invoker
// sees, so a runaway opencode child gets the same SIGTERM treatment as a
// per-invoke timeout.
func TestShutdown_AbortInFlightCancelsExecContext(t *testing.T) {
	inv := &blockingInvoker{
		release:      make(chan struct{}),
		ctxObserved:  make(chan context.Context, 1),
		finalOutcome: opencode.OutcomeCrash,
	}
	defer close(inv.release) // safety net

	core, _, _ := newCore(t, inv)

	core.Dispatch(context.Background(), adapter.Event{Message: &adapter.MessageEvent{
		Platform: "discord", ThreadID: "ch-1", PlatformMessageID: "m-abort",
		Author: adapter.Author{Label: "alice"}, Body: "ping", Mention: true,
	}})

	var execCtx context.Context
	select {
	case execCtx = <-inv.ctxObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("invocation never started")
	}

	if execCtx.Err() != nil {
		t.Fatal("exec context cancelled before AbortInFlight")
	}
	core.AbortInFlight()

	select {
	case <-execCtx.Done():
		if !errors.Is(execCtx.Err(), context.Canceled) {
			t.Fatalf("unexpected ctx err: %v", execCtx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("exec ctx not cancelled after AbortInFlight")
	}

	// Drain the now-cancelled invocation cleanly.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !core.WaitDrain(ctx) {
		t.Fatal("WaitDrain did not return after AbortInFlight unblocked Invoke")
	}
}
