package bot

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/opencode"
)

// gatedInvoker yields one result per call but only after release is closed,
// so that bursts of triggers can pile up at the queue while the first one
// is "in flight". Captures the final UserMsg seen by each call so the
// coalesce test can assert which message Espur actually used.
type gatedInvoker struct {
	release   chan struct{}
	startedCh chan struct{}
	calls     atomic.Int32
	lastMsg   atomic.Value // string
}

func (g *gatedInvoker) Invoke(ctx context.Context, req opencode.Request) (opencode.Result, error) {
	g.lastMsg.Store(req.UserMsg)
	g.calls.Add(1)
	select {
	case g.startedCh <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return opencode.Result{}, ctx.Err()
	}
	return opencode.Result{Outcome: opencode.OutcomeSuccess, AssistantText: "ok"}, nil
}

// TestBot_BurstCoalesce_KeepsLatestOnly verifies the spec contract: when 3
// mention messages arrive on one thread while the first is in flight, the
// queue runs the first, then runs exactly ONE more invocation that uses
// the most-recent body — the middle messages are dropped (their dedup rows
// stay, so a resend won't double-fire).
func TestBot_BurstCoalesce_KeepsLatestOnly(t *testing.T) {
	inv := &gatedInvoker{
		release:   make(chan struct{}),
		startedCh: make(chan struct{}, 4),
	}
	core, fa, _ := newCore(t, inv)
	ctx := context.Background()

	mk := func(id, body string) *adapter.MessageEvent {
		return &adapter.MessageEvent{
			Platform: "discord", ThreadID: "ch-1", PlatformMessageID: id,
			Author: adapter.Author{Label: "alice"}, Body: body, Mention: true,
		}
	}

	// First message kicks off the in-flight invocation.
	core.Dispatch(ctx, adapter.Event{Message: mk("m-1", "first")})
	select {
	case <-inv.startedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("first invocation never started")
	}

	// Two more land during in-flight. Spec: first one fills the coalesce
	// slot (and gets a "still thinking" ack), second overwrites the slot.
	core.Dispatch(ctx, adapter.Event{Message: mk("m-2", "middle")})
	core.Dispatch(ctx, adapter.Event{Message: mk("m-3", "latest")})

	// Release the first invocation; it posts "ok", queue drains the
	// coalesce slot which kicks off invocation #2.
	close(inv.release)

	// We expect 1 ack + 1 first reply + 1 coalesced-run reply = 3 posts
	// going to the fakeAdapter. The "still thinking" ack is posted from
	// a goroutine on submit, so the exact order can vary; we just count.
	posted := 0
	deadline := time.After(3 * time.Second)
	for posted < 3 {
		select {
		case <-fa.done:
			posted++
		case <-deadline:
			t.Fatalf("only %d posts in time (want 3)", posted)
		}
	}

	if got := int(inv.calls.Load()); got != 2 {
		t.Fatalf("expected exactly 2 Invoke calls (first + coalesced), got %d", got)
	}
	last, _ := inv.lastMsg.Load().(string)
	// The <request> block must contain "latest" (the most recent body)
	// not "middle" (the overwritten coalesce slot). The thread-context
	// tail DOES contain "middle" by design — every user message lands in
	// the transcript regardless of whether it triggers an invocation.
	reqBody := extractRequest(last)
	if !strings.Contains(reqBody, "latest") {
		t.Fatalf("coalesced run must use the LATEST body; <request>=%q", reqBody)
	}
	if strings.Contains(reqBody, "middle") {
		t.Fatalf("<request> should not carry overwritten coalesce body; got %q", reqBody)
	}
}

// extractRequest pulls the body out of a `<request from="…">…</request>`
// block, so tests can assert what the model actually sees as the "act on
// this" message (vs. the thread-context tail).
func extractRequest(msg string) string {
	i := strings.Index(msg, "<request")
	if i < 0 {
		return ""
	}
	j := strings.Index(msg[i:], ">")
	if j < 0 {
		return ""
	}
	start := i + j + 1
	k := strings.Index(msg[start:], "</request>")
	if k < 0 {
		return msg[start:]
	}
	return msg[start : start+k]
}

// TestBot_DispatchLifecycleEvent ensures lifecycle events route through
// Dispatch without panicking and produce no Post. This is a smoke test
// against the regression where lifecycle handling can drop into the
// message branch if the sum-type discrimination breaks.
func TestBot_DispatchLifecycleEvent(t *testing.T) {
	inv := &fakeInvoker{} // no invocation expected
	core, fa, _ := newCore(t, inv)

	core.Dispatch(context.Background(), adapter.Event{Lifecycle: &adapter.LifecycleEvent{
		Platform: "discord", Kind: adapter.LifecycleConnected, At: time.Now(),
	}})

	select {
	case <-fa.done:
		t.Fatal("lifecycle event should not produce a Post")
	case <-time.After(100 * time.Millisecond):
	}
}
