package bot

import (
	"context"

	"github.com/punny/espur/internal/adapter"
)

// threadQueue serializes trigger processing for one thread. At most one trigger
// is processed at a time; at most one more waits in the coalesce slot (newest
// wins). See docs/specs/trigger.dog.md.
//
// running and coalesce are guarded by Core.mu — the same lock that owns the
// queues map — so "is a worker running?" and "is this queue still in the map?"
// stay consistent. A queue is removed from the map by its own worker, atomically
// with clearing running, the instant it goes idle. That bounds live goroutines
// to the number of *active* threads, not every thread ever seen.
type threadQueue struct {
	core     *Core
	key      string
	platform string
	threadID string

	// Guarded by core.mu:
	running  bool
	coalesce *adapter.MessageEvent
}

// enqueue routes a mention-bearing message to its thread queue: it starts a
// worker if the thread is idle, or coalesces onto the running worker otherwise.
//
// The drain WaitGroup is incremented here, under core.mu, BEFORE the worker is
// observable — and the shutdown barrier (stopped) is re-checked under the same
// lock. That is what makes the shutdown drain race-free: StopAccepting and this
// Add are mutually exclusive, so a message either (a) Adds before stopped is
// set, and WaitDrain waits for it, or (b) sees stopped and is dropped. There is
// no window where a worker starts uncounted after WaitDrain has returned.
func (c *Core) enqueue(ctx context.Context, m *adapter.MessageEvent) {
	key := m.Platform + ":" + m.ThreadID
	c.mu.Lock()
	if c.stopped.Load() {
		c.mu.Unlock()
		return
	}
	q, ok := c.queues[key]
	if !ok {
		q = &threadQueue{core: c, key: key, platform: m.Platform, threadID: m.ThreadID}
		c.queues[key] = q
	}
	if !q.running {
		q.running = true
		c.inflight.Add(1)
		c.mu.Unlock()
		go q.work(m)
		return
	}
	firstCoalesce := q.coalesce == nil
	q.coalesce = m
	c.mu.Unlock()
	if firstCoalesce {
		// Best-effort "still thinking" ack — one per coalesced run.
		go q.ack(ctx, m.ThreadID)
	}
}

// work processes the thread's messages serially until the coalesce slot is
// empty, then retires the queue (clears running, removes itself from the map)
// and returns. The single deferred Done balances the one Add in enqueue,
// regardless of how many coalesced messages this worker handles.
func (q *threadQueue) work(m *adapter.MessageEvent) {
	defer q.core.inflight.Done()
	for {
		// Detached exec context so a first SIGTERM (which cancels the dispatch
		// ctx) does not yank an in-flight invocation; it is bounded by the
		// per-invoke opencode timeout instead. See docs/specs/shutdown.dog.md.
		execCtx, cancel := q.core.execContext()
		q.core.HandleTrigger(execCtx, m)
		cancel()

		q.core.mu.Lock()
		// A coalesced message gets its run only if shutdown hasn't begun.
		// Once draining, coalesced-waiting triggers are dropped — they were
		// never started, and starting work after the signal would violate the
		// "no new work" contract. See docs/specs/shutdown.dog.md.
		if q.coalesce != nil && !q.core.stopped.Load() {
			m = q.coalesce
			q.coalesce = nil
			q.core.mu.Unlock()
			continue
		}
		q.running = false
		delete(q.core.queues, q.key)
		q.core.mu.Unlock()
		return
	}
}

func (q *threadQueue) ack(ctx context.Context, threadID string) {
	a := q.core.posters[q.platform]
	if a == nil {
		return
	}
	_, _ = a.Post(ctx, threadID, "still thinking, will use your latest message")
}
