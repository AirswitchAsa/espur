// Package bot is the core orchestrator: dedup, per-thread queue with
// burst-coalesce, dispatch to context-assembly → vendor pool → reply →
// transcript. See docs/specs/trigger.dog.md and docs/specs/reply.dog.md.
package bot

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/contextasm"
	"github.com/punny/espur/internal/memory"
	"github.com/punny/espur/internal/obs"
	"github.com/punny/espur/internal/store"
	"github.com/punny/espur/internal/transcript"
	"github.com/punny/espur/internal/vendor"
)

// Config bundles the dependencies/options for Core.
type Config struct {
	DB              *store.DB
	Pool            *vendor.Pool
	Transcript      *transcript.Store
	DashboardURL    string
	InvokeTimeout   time.Duration // per opencode-invoke.dog.md default 120s
	TranscriptTailN int           // context-assembly.dog.md default 30
	Logger          *slog.Logger
}

// Core glues adapters to the rest of Espur.
type Core struct {
	cfg     Config
	mu      sync.Mutex
	queues  map[string]*threadQueue    // keyed by "<platform>:<thread_id>"
	posters map[string]adapter.Adapter // platform → adapter for outbound Post

	// Shutdown bookkeeping. inflight tracks how many HandleTrigger calls are
	// currently running; stopped flips to true once shutdown begins and is
	// used by Dispatch to reject new triggers. See docs/specs/shutdown.dog.md.
	inflight   sync.WaitGroup
	stopped    atomic.Bool
	hardCtx    context.Context
	hardCancel context.CancelFunc
}

// New constructs a Core. Register adapters with RegisterAdapter before Run.
func New(cfg Config) *Core {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.InvokeTimeout <= 0 {
		cfg.InvokeTimeout = 120 * time.Second
	}
	if cfg.TranscriptTailN <= 0 {
		cfg.TranscriptTailN = contextasm.DefaultTailN
	}
	return &Core{
		cfg:     cfg,
		queues:  map[string]*threadQueue{},
		posters: map[string]adapter.Adapter{},
	}
}

// RegisterAdapter wires an adapter into the core. Must be called before
// Dispatch is called for that platform.
func (c *Core) RegisterAdapter(a adapter.Adapter) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.posters[a.Platform()] = a
}

// Dispatch is the single entry point for inbound adapter events. Returns
// only after the event has been routed (queued or dropped); the underlying
// processing happens in a per-thread goroutine.
func (c *Core) Dispatch(ctx context.Context, ev adapter.Event) {
	switch {
	case ev.Message != nil:
		c.onMessage(ctx, ev.Message)
	case ev.Lifecycle != nil:
		evName := obs.AdapterLifecycle
		switch ev.Lifecycle.Kind {
		case adapter.LifecycleConnected:
			evName = obs.AdapterConnected
		case adapter.LifecycleDisconnected:
			evName = obs.AdapterDisconnected
		case adapter.LifecycleReconnecting:
			evName = obs.AdapterReconnecting
		}
		c.cfg.Logger.Info("adapter lifecycle",
			"event", evName,
			"platform", ev.Lifecycle.Platform, "kind", ev.Lifecycle.Kind,
			"cause", ev.Lifecycle.Cause, "attempt", ev.Lifecycle.Attempt)
	}
}

// execContext returns a context detached from the signal-cancellation tree.
// In-flight invocations should outlive a first SIGTERM (per shutdown.dog.md);
// they're bounded by the per-invoke opencode timeout instead. The parent
// is c.hardCtx (lazily initialised) so that AbortInFlight can yank every
// running invocation at once on second-signal escalation.
func (c *Core) execContext() (context.Context, context.CancelFunc) {
	c.mu.Lock()
	if c.hardCtx == nil {
		c.hardCtx, c.hardCancel = context.WithCancel(context.Background())
	}
	parent := c.hardCtx
	c.mu.Unlock()
	return context.WithCancel(parent)
}

// AbortInFlight cancels every in-flight HandleTrigger invocation. Used by the
// shutdown sequencer on a second termination signal or after the drain
// deadline lapses. Idempotent.
func (c *Core) AbortInFlight() {
	c.mu.Lock()
	cancel := c.hardCancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StopAccepting marks the core as draining: Dispatch will reject new
// triggers, but in-flight HandleTrigger calls keep running until WaitDrain
// returns. Idempotent.
//
// Sets the flag under c.mu so it is mutually exclusive with enqueue's
// inflight.Add — this is what closes the shutdown drain race (see enqueue).
func (c *Core) StopAccepting() {
	c.mu.Lock()
	c.stopped.Store(true)
	c.mu.Unlock()
}

// WaitDrain blocks until all in-flight HandleTrigger calls complete or ctx
// expires. Returns true if the drain finished cleanly, false on deadline.
func (c *Core) WaitDrain(ctx context.Context) bool {
	done := make(chan struct{})
	go func() { c.inflight.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *Core) onMessage(ctx context.Context, m *adapter.MessageEvent) {
	if c.stopped.Load() {
		// Phase 1 of shutdown: no new triggers, but adapters may still hand
		// us events from buffered channels. Drop silently — the platform's
		// retry on the next boot will dedupe against the table.
		return
	}
	// Dedup first (spec: trigger.dog.md). Dedup applies regardless of whether
	// the message mentions the bot — non-mention messages still get appended
	// to the transcript.
	first, err := c.cfg.DB.SeenMessage(ctx, m.Platform, m.PlatformMessageID)
	if err != nil {
		c.cfg.Logger.Error("dedup write failed", "err", err)
		return
	}
	if !first {
		return // already processed
	}

	// Append transcript record (kind=user) for every accepted message, mention
	// or not — spec: transcript.dog.md "Write rules".
	if err := c.cfg.Transcript.Append(m.Platform, m.ThreadID, transcript.Record{
		Kind:              transcript.KindUser,
		PlatformMessageID: m.Platform + ":" + m.PlatformMessageID,
		Author:            transcript.Author{ID: m.Author.ID, Label: m.Author.Label},
		Body:              m.Body,
		Meta:              transcript.Meta{Mention: m.Mention},
	}); err != nil {
		c.cfg.Logger.Error("transcript append failed", "err", err)
		// Fail closed for the trigger; the dedup row is already written, but
		// the spec accepts that the user will resend.
		return
	}
	if !m.Mention {
		c.cfg.Logger.Debug("non-mention observed",
			"event", obs.TriggerObserved,
			"platform", m.Platform, "thread_id", m.ThreadID)
		return // observed-only; no trigger.
	}
	c.cfg.Logger.Info("trigger accepted",
		"event", obs.TriggerAccepted,
		"platform", m.Platform, "thread_id", m.ThreadID,
		"message_id", m.PlatformMessageID)

	// Enqueue / coalesce. See queue.go.
	c.enqueue(ctx, m)
}

// PostAdapterEvent is exposed for adapters wishing to push pre-built events
// (e.g. tests). Currently no production caller other than the dispatch fan-in.
func (c *Core) PostAdapterEvent(ctx context.Context, ev adapter.Event) { c.Dispatch(ctx, ev) }

// HandleTrigger is the actual work for one accepted, mention-bearing message:
// memory seed → context assembly → vendor pool → reply → transcript.
// Exposed at the package level for testability.
func (c *Core) HandleTrigger(ctx context.Context, m *adapter.MessageEvent) {
	workDir := c.cfg.Transcript.ThreadDir(m.Platform, m.ThreadID)
	if err := memory.EnsureWorkDir(workDir); err != nil {
		c.cfg.Logger.Error("workdir seed failed", "err", err)
		c.postCrash(ctx, m, "workdir_seed_failed")
		return
	}

	// Read transcript tail (user messages only, chronological). The current
	// message is already persisted; exclude it from the context block.
	tail, err := c.cfg.Transcript.TailUserMessages(m.Platform, m.ThreadID, c.cfg.TranscriptTailN+1)
	if err != nil {
		c.cfg.Logger.Error("transcript tail read failed", "err", err)
		c.postCrash(ctx, m, "transcript_read_failed")
		return
	}
	// Drop the current message from the tail (it's the last record).
	if n := len(tail); n > 0 && tail[n-1].PlatformMessageID == m.Platform+":"+m.PlatformMessageID {
		tail = tail[:n-1]
	}
	if len(tail) > c.cfg.TranscriptTailN {
		tail = tail[len(tail)-c.cfg.TranscriptTailN:]
	}

	// AGENTS.md is part of the stable per-thread prefix; missing/unreadable
	// is non-fatal — we just skip the memory block.
	agentsMD, _ := os.ReadFile(filepath.Join(workDir, "AGENTS.md"))
	userMsg := contextasm.Assemble(contextasm.Prefix{
		Platform: m.Platform,
		ThreadID: m.ThreadID,
		AgentsMD: string(agentsMD),
	}, tail, contextasm.Trigger{
		AuthorLabel: m.Author.Label,
		Body:        m.Body,
	})

	res, err := c.cfg.Pool.Run(ctx, workDir, userMsg, c.cfg.InvokeTimeout)
	if err != nil {
		c.cfg.Logger.Error("vendor pool failed", "err", err, "outcome", res.Outcome)
	}

	a := c.posters[m.Platform]
	if a == nil {
		c.cfg.Logger.Error("no adapter registered", "platform", m.Platform)
		return
	}

	switch res.Outcome {
	case vendor.OutcomeSuccess:
		c.cfg.Logger.Info("invocation success",
			"event", obs.InvocationSuccess,
			"platform", m.Platform, "thread_id", m.ThreadID,
			"vendor_id", res.VendorID, "attempts", len(res.Attempts))
		pid, perr := a.Post(ctx, m.ThreadID, res.Text)
		c.appendBotReply(m, pid, res.Text, "success", "", res.VendorID, perr)

	case vendor.OutcomeTimeout:
		rid := NewRequestID()
		c.cfg.Logger.Error("invocation timeout",
			"event", obs.InvocationTimeout,
			"request_id", rid,
			"platform", m.Platform, "thread_id", m.ThreadID,
			"vendor_id", res.VendorID)
		pid, perr := a.Post(ctx, m.ThreadID, TimeoutReply)
		c.appendBotReply(m, pid, TimeoutReply, "timeout", rid, res.VendorID, perr)

	case vendor.OutcomeAllDrained:
		rid := NewRequestID()
		c.cfg.Logger.Error("all vendors drained",
			"event", obs.InvocationAllDrained,
			"request_id", rid,
			"platform", m.Platform, "thread_id", m.ThreadID,
			"penalized", len(res.Penalized))
		body := DrainedReply(res.Penalized, c.cfg.DashboardURL, time.Now())
		pid, perr := a.Post(ctx, m.ThreadID, body)
		c.appendBotReply(m, pid, body, "drained", rid, "", perr)

	case vendor.OutcomeCrash:
		rid := NewRequestID()
		c.cfg.Logger.Error("invocation crash",
			"event", obs.InvocationCrash,
			"request_id", rid, "vendor_id", res.VendorID,
			"reason", res.CrashReason, "attempts", len(res.Attempts))
		body := CrashReply(rid)
		pid, perr := a.Post(ctx, m.ThreadID, body)
		c.appendBotReply(m, pid, body, "crash", rid, res.VendorID, perr)

	default:
		c.postCrash(ctx, m, "unknown_outcome")
	}
}

func (c *Core) postCrash(ctx context.Context, m *adapter.MessageEvent, reason string) {
	a := c.posters[m.Platform]
	if a == nil {
		return
	}
	rid := NewRequestID()
	c.cfg.Logger.Error("crash reply",
		"event", obs.InvocationCrash,
		"request_id", rid, "reason", reason,
		"platform", m.Platform, "thread_id", m.ThreadID)
	body := CrashReply(rid)
	pid, perr := a.Post(ctx, m.ThreadID, body)
	c.appendBotReply(m, pid, body, "crash", rid, "", perr)
}

func (c *Core) appendBotReply(m *adapter.MessageEvent, platformMsgID, body, outcome, reqID, vendorID string, postErr error) {
	if postErr != nil {
		c.cfg.Logger.Error("adapter post failed",
			"event", obs.AdapterPostFailed,
			"platform", m.Platform, "thread_id", m.ThreadID,
			"outcome", outcome, "request_id", reqID, "err", postErr.Error())
		// Per spec: if no chunk was posted at all, write a system "previous
		// turn aborted" line so the next invocation sees the gap.
		if platformMsgID == "" {
			_ = c.cfg.Transcript.Append(m.Platform, m.ThreadID, transcript.Record{
				Kind: transcript.KindSystem,
				Body: "previous turn aborted: adapter post failed",
				Meta: transcript.Meta{Note: "previous-turn-aborted", RequestID: reqID},
			})
			return
		}
	}
	_ = c.cfg.Transcript.Append(m.Platform, m.ThreadID, transcript.Record{
		Kind:              transcript.KindBot,
		PlatformMessageID: platformMsgID,
		Author:            transcript.Author{ID: "bot", Label: "espur"},
		Body:              body,
		Meta: transcript.Meta{
			ReplyOutcome: outcome, RequestID: reqID, VendorID: vendorID,
		},
	})
}
