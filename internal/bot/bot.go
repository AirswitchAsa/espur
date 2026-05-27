// Package bot is the core orchestrator: dedup, per-thread queue with
// burst-coalesce, dispatch to context-assembly → vendor pool → reply →
// transcript. See specs/trigger.dog.md and specs/reply.dog.md.
package bot

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/punny/espur/internal/adapter"
	"github.com/punny/espur/internal/contextasm"
	"github.com/punny/espur/internal/memory"
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
		c.cfg.Logger.Info("adapter lifecycle",
			"platform", ev.Lifecycle.Platform, "kind", ev.Lifecycle.Kind,
			"cause", ev.Lifecycle.Cause, "attempt", ev.Lifecycle.Attempt)
	}
}

func (c *Core) onMessage(ctx context.Context, m *adapter.MessageEvent) {
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
		return // observed-only; no trigger.
	}

	// Enqueue / coalesce.
	key := m.Platform + ":" + m.ThreadID
	c.mu.Lock()
	q, ok := c.queues[key]
	if !ok {
		q = &threadQueue{
			core:     c,
			platform: m.Platform,
			threadID: m.ThreadID,
			incoming: make(chan *adapter.MessageEvent, 1),
			coalesce: nil,
		}
		c.queues[key] = q
		go q.loop(ctx)
	}
	c.mu.Unlock()
	q.submit(ctx, m)
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

	userMsg := contextasm.Assemble(tail, contextasm.Trigger{
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
		pid, perr := a.Post(ctx, m.ThreadID, res.Text)
		c.appendBotReply(m, pid, res.Text, "success", "", res.VendorID, perr)

	case vendor.OutcomeTimeout:
		pid, perr := a.Post(ctx, m.ThreadID, TimeoutReply)
		c.appendBotReply(m, pid, TimeoutReply, "timeout", "", res.VendorID, perr)

	case vendor.OutcomeAllDrained:
		body := DrainedReply(res.Penalized, c.cfg.DashboardURL, time.Now())
		pid, perr := a.Post(ctx, m.ThreadID, body)
		c.appendBotReply(m, pid, body, "drained", "", "", perr)

	case vendor.OutcomeCrash:
		rid := NewRequestID()
		c.cfg.Logger.Error("crash reply", "request_id", rid, "vendor_id", res.VendorID,
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
	c.cfg.Logger.Error("crash reply", "request_id", rid, "reason", reason)
	body := CrashReply(rid)
	pid, perr := a.Post(ctx, m.ThreadID, body)
	c.appendBotReply(m, pid, body, "crash", rid, "", perr)
}

func (c *Core) appendBotReply(m *adapter.MessageEvent, platformMsgID, body, outcome, reqID, vendorID string, postErr error) {
	if postErr != nil {
		c.cfg.Logger.Error("adapter post failed", "err", postErr, "outcome", outcome)
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
