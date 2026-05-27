package bot

import (
	"context"
	"sync"

	"github.com/punny/espur/internal/adapter"
)

// threadQueue serializes trigger processing per thread. At most one trigger
// is in-flight; at most one is waiting (coalesced) per spec/trigger.dog.md.
type threadQueue struct {
	core     *Core
	platform string
	threadID string

	mu       sync.Mutex
	inflight bool
	coalesce *adapter.MessageEvent // waiting slot (one only)
	incoming chan *adapter.MessageEvent
}

// submit enqueues a message, possibly coalescing it.
func (q *threadQueue) submit(ctx context.Context, m *adapter.MessageEvent) {
	q.mu.Lock()
	if !q.inflight {
		q.inflight = true
		q.mu.Unlock()
		select {
		case q.incoming <- m:
		case <-ctx.Done():
		}
		return
	}
	if q.coalesce == nil {
		q.coalesce = m
		q.mu.Unlock()
		// Best-effort "still thinking" ack — one per coalesced run.
		go q.ack(ctx, m.ThreadID)
		return
	}
	// Already coalescing: replace text with newer message, keep newer ids.
	q.coalesce = m
	q.mu.Unlock()
}

func (q *threadQueue) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-q.incoming:
			q.core.HandleTrigger(ctx, m)
			q.mu.Lock()
			next := q.coalesce
			q.coalesce = nil
			if next != nil {
				q.mu.Unlock()
				select {
				case q.incoming <- next:
				case <-ctx.Done():
					return
				}
				continue
			}
			q.inflight = false
			q.mu.Unlock()
		}
	}
}

func (q *threadQueue) ack(ctx context.Context, threadID string) {
	a := q.core.posters[q.platform]
	if a == nil {
		return
	}
	_, _ = a.Post(ctx, threadID, "still thinking, will use your latest message")
}
