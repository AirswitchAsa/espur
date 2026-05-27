package vendor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/punny/espur/internal/opencode"
	"github.com/punny/espur/internal/store"
)

// gateInvoker counts concurrent in-flight Invoke calls so the semaphore test
// can observe the cap directly. block holds every invocation until release
// is closed, so all goroutines pile up at the gate simultaneously.
type gateInvoker struct {
	inFlight atomic.Int32
	peak     atomic.Int32
	release  chan struct{}
}

func (g *gateInvoker) Invoke(ctx context.Context, _ opencode.Request) (opencode.Result, error) {
	now := g.inFlight.Add(1)
	for {
		peak := g.peak.Load()
		if now <= peak || g.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	defer g.inFlight.Add(-1)
	select {
	case <-g.release:
	case <-ctx.Done():
		return opencode.Result{Outcome: opencode.OutcomeCrash}, ctx.Err()
	}
	return opencode.Result{Outcome: opencode.OutcomeSuccess, AssistantText: "ok"}, nil
}

// TestPool_ConcurrencyCap_Bounds verifies that with N parallel Run calls and
// a cap of K, at most K opencode invocations are ever in flight at once.
// This is the regression guard against re-introducing the global serializing
// mutex that used to live in Pool.Run.
func TestPool_ConcurrencyCap_Bounds(t *testing.T) {
	const callers = 12
	const cap = 3

	inv := &gateInvoker{release: make(chan struct{})}
	p, db := newTestPool(t, inv)
	p.WithMaxConcurrent(cap)
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: "v1", Model: "m1", Enabled: true, Position: 0, CredKind: "byo_key",
	})

	// Start N parallel Runs; they'll all stack up at the gate.
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Run(ctx, t.TempDir(), "x", 5*time.Second)
		}()
	}

	// Give the goroutines a moment to all reach the semaphore + Invoke.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(inv.inFlight.Load()) >= cap {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := int(inv.inFlight.Load()); got != cap {
		t.Fatalf("expected exactly %d in-flight at saturation, got %d", cap, got)
	}

	close(inv.release)
	wg.Wait()

	if peak := int(inv.peak.Load()); peak > cap {
		t.Fatalf("peak in-flight %d exceeds cap %d", peak, cap)
	}
}

// TestPool_NoConcurrencyCap permits unbounded parallelism when the cap is
// disabled (n <= 0). This is the escape hatch documented on WithMaxConcurrent.
func TestPool_NoConcurrencyCap(t *testing.T) {
	const callers = 8

	inv := &gateInvoker{release: make(chan struct{})}
	p, db := newTestPool(t, inv)
	p.WithMaxConcurrent(0) // disable
	ctx := context.Background()
	_ = db.UpsertVendor(ctx, store.Vendor{
		VendorID: "v1", Model: "m1", Enabled: true, Position: 0, CredKind: "byo_key",
	})

	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Run(ctx, t.TempDir(), "x", 5*time.Second)
		}()
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if int(inv.inFlight.Load()) >= callers {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := int(inv.inFlight.Load()); got != callers {
		t.Fatalf("expected all %d callers in flight, got %d", callers, got)
	}
	close(inv.release)
	wg.Wait()
}
