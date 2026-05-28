package vendor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/punny/espur/internal/obs"
	"github.com/punny/espur/internal/opencode"
	"github.com/punny/espur/internal/secrets"
	"github.com/punny/espur/internal/store"
)

// Invoker is the surface this package needs from internal/opencode. Defined
// here so tests can substitute a fake without spawning real children.
type Invoker interface {
	Invoke(ctx context.Context, req opencode.Request) (opencode.Result, error)
}

// realInvoker bridges the function-style API of internal/opencode to the
// interface above.
type realInvoker struct{}

func (realInvoker) Invoke(ctx context.Context, req opencode.Request) (opencode.Result, error) {
	return opencode.Invoke(ctx, req)
}

// DefaultMaxConcurrent is the default global cap on concurrent opencode
// children across all per-thread queues. Per-thread serialization is the
// adapter/queue's job; this cap bounds total resource use on the host.
// Overridable via ESPUR_OPENCODE_MAX_CONCURRENT at the call site.
const DefaultMaxConcurrent = 4

// Pool is the live vendor pool, backed by the store. Safe for concurrent use.
//
// Per-vendor penalty state can race in theory (two concurrent Runs both see
// a vendor as eligible and both invoke it). The races are benign: PutPenalty
// is a full-row upsert, so the worst case is FailureStreak over-counting by
// one. Cooldown still fires correctly. See docs/specs/vendor-pool.dog.md.
type Pool struct {
	db      *store.DB
	vault   *secrets.Vault
	invoker Invoker
	now     func() time.Time
	rng     *rand.Rand
	rngMu   sync.Mutex // math/rand.Rand is not safe for concurrent use
	logger  *slog.Logger
	sem     chan struct{} // global concurrency bound; nil disables the cap
}

// New constructs a pool with the real opencode invoker and default clock,
// with the global concurrency cap at DefaultMaxConcurrent.
func New(db *store.DB, vault *secrets.Vault) *Pool {
	return &Pool{
		db:      db,
		vault:   vault,
		invoker: realInvoker{},
		now:     time.Now,
		rng:     rand.New(rand.NewSource(time.Now().UnixNano())),
		logger:  slog.Default(),
		sem:     make(chan struct{}, DefaultMaxConcurrent),
	}
}

// WithMaxConcurrent sets the global cap on concurrent opencode children.
// n <= 0 disables the cap (no semaphore). Returns p for chaining.
func (p *Pool) WithMaxConcurrent(n int) *Pool {
	if n <= 0 {
		p.sem = nil
	} else {
		p.sem = make(chan struct{}, n)
	}
	return p
}

// WithLogger swaps the logger used for vendor-pool transitions.
func (p *Pool) WithLogger(l *slog.Logger) *Pool {
	if l != nil {
		p.logger = l
	}
	return p
}

// WithInvoker swaps the invoker (used by tests). Returns p for chaining.
func (p *Pool) WithInvoker(inv Invoker) *Pool { p.invoker = inv; return p }

// WithClock swaps the clock (used by tests).
func (p *Pool) WithClock(fn func() time.Time) *Pool { p.now = fn; return p }

// Attempt is one logged vendor try. Useful for tests and the all-drained reply
// (which enumerates which vendors were penalized and why).
type Attempt struct {
	VendorID    string
	Outcome     opencode.Outcome // success/timeout/crash
	Class       FailureClass
	CrashReason string
}

// Result is the terminal outcome of a single Run invocation across all
// vendors in the pool.
type Result struct {
	Outcome     Outcome
	VendorID    string              // populated on Success / Timeout / Crash
	Text        string              // assistant text on Success
	CrashReason string              // set on Crash
	Attempts    []Attempt           // full audit
	Penalized   []PenalizedSnapshot // populated on AllDrained — for the user-visible reply
}

// Outcome is the top-level result of the pool loop. Distinct from
// opencode.Outcome because the pool can decide "all drained" without any
// child running.
type Outcome int

const (
	OutcomeUnknown Outcome = iota
	OutcomeSuccess
	OutcomeTimeout
	OutcomeAllDrained
	OutcomeCrash
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeTimeout:
		return "timeout"
	case OutcomeAllDrained:
		return "drained"
	case OutcomeCrash:
		return "crash"
	default:
		return "unknown"
	}
}

// PenalizedSnapshot describes a vendor's penalty state at the moment of an
// all-drained reply, for [[reply]] enumeration.
type PenalizedSnapshot struct {
	VendorID      string
	Status        store.PenaltyStatus
	CooldownUntil *time.Time
}

// Run walks the priority list and attempts vendors until one succeeds, all are
// exhausted, or a non-fallthrough outcome (timeout / crash) ends the loop.
// userMsg is the composite message from internal/context (already wrapped).
// workDir is the per-thread working directory.
func (p *Pool) Run(ctx context.Context, workDir, userMsg string, timeout time.Duration) (Result, error) {
	// Global concurrency cap. Acquire before any vendor walking — once we
	// hold a slot, the rest of this call is allowed to spawn at most one
	// opencode child at a time (the per-attempt switch inside the loop is
	// sequential).
	if p.sem != nil {
		select {
		case p.sem <- struct{}{}:
			defer func() { <-p.sem }()
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}

	vendors, err := p.db.ListVendors(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("pool: list vendors: %w", err)
	}

	res := Result{}
	exhaustedAll := true

	for _, v := range vendors {
		if !v.Enabled {
			continue
		}
		pen, err := p.db.GetPenalty(ctx, v.VendorID)
		if err != nil {
			return Result{}, err
		}
		if !isEligible(pen, p.now()) {
			res.Penalized = append(res.Penalized, PenalizedSnapshot{
				VendorID: v.VendorID, Status: pen.Status, CooldownUntil: pen.CooldownUntil,
			})
			continue
		}
		exhaustedAll = false

		credEnv, err := p.loadCredEnv(ctx, v.VendorID)
		if err != nil {
			// Decrypt or fetch error — treat as a crash for this attempt,
			// not a vendor failure (it's our side). Don't penalize.
			res.Attempts = append(res.Attempts, Attempt{
				VendorID: v.VendorID, Outcome: opencode.OutcomeCrash,
				CrashReason: "credential_load_failed",
			})
			res.Outcome = OutcomeCrash
			res.VendorID = v.VendorID
			res.CrashReason = "credential_load_failed"
			return res, err
		}

		ocReq := opencode.Request{
			Vendor: opencode.Vendor{
				VendorID: v.VendorID, Model: v.Model, CredEnv: credEnv,
			},
			WorkDir: workDir,
			UserMsg: userMsg,
			Timeout: timeout,
		}
		ocRes, ocErr := p.invoker.Invoke(ctx, ocReq)
		att := Attempt{VendorID: v.VendorID, Outcome: ocRes.Outcome, CrashReason: ocRes.CrashReason}

		switch ocRes.Outcome {
		case opencode.OutcomeSuccess:
			// Spec: success resets streak + status.
			if pen.Status != store.PenaltyEligible || pen.FailureStreak > 0 {
				p.logger.Info("vendor recovered",
					"event", obs.VendorRecovered,
					"vendor_id", v.VendorID, "prior_streak", pen.FailureStreak)
			}
			_ = p.db.PutPenalty(ctx, applySuccess(pen, p.now()))
			att.Class = ClassNone
			res.Attempts = append(res.Attempts, att)
			res.Outcome = OutcomeSuccess
			res.VendorID = v.VendorID
			res.Text = ocRes.AssistantText
			return res, nil

		case opencode.OutcomeTimeout:
			// Spec: timeout does NOT mutate penalty state, does NOT fall through.
			res.Attempts = append(res.Attempts, att)
			res.Outcome = OutcomeTimeout
			res.VendorID = v.VendorID
			return res, nil

		case opencode.OutcomeCrash:
			// Classify by inspecting stdout/stderr. If it's a known fallthrough
			// pattern, penalize + try next vendor. Otherwise terminate as crash.
			class := Classify(ocRes.Stdout, ocRes.Stderr)
			att.Class = class
			res.Attempts = append(res.Attempts, att)
			if class == ClassNone {
				// Genuine crash — propagate.
				res.Outcome = OutcomeCrash
				res.VendorID = v.VendorID
				res.CrashReason = ocRes.CrashReason
				return res, ocErr
			}
			// Vendor-side failure: penalize and try the next eligible vendor.
			p.rngMu.Lock()
			updated := applyFailure(pen, class, p.now(), p.rng)
			p.rngMu.Unlock()
			if err := p.db.PutPenalty(ctx, updated); err != nil {
				return res, err
			}
			if updated.Status == store.PenaltyAuthLocked {
				p.logger.Warn("vendor auth locked",
					"event", obs.VendorAuthLocked,
					"vendor_id", v.VendorID, "failure_class", class.String())
			} else if updated.Status == store.PenaltyCooldown {
				cooldownUntil := ""
				if updated.CooldownUntil != nil {
					cooldownUntil = updated.CooldownUntil.UTC().Format(time.RFC3339)
				}
				p.logger.Info("vendor entered cooldown",
					"event", obs.VendorCooldownEntered,
					"vendor_id", v.VendorID, "failure_class", class.String(),
					"failure_streak", updated.FailureStreak,
					"cooldown_until", cooldownUntil)
			}
			res.Penalized = append(res.Penalized, PenalizedSnapshot{
				VendorID: v.VendorID, Status: updated.Status, CooldownUntil: updated.CooldownUntil,
			})
			continue
		}
	}

	if exhaustedAll && len(res.Attempts) == 0 {
		// Never even tried — every vendor was already in penalty.
		res.Outcome = OutcomeAllDrained
		return res, nil
	}
	// Walked the whole list with fallthrough failures.
	res.Outcome = OutcomeAllDrained
	return res, nil
}

func (p *Pool) loadCredEnv(ctx context.Context, vendorID string) (map[string]string, error) {
	c, err := p.db.GetCredential(ctx, "vendor", vendorID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if len(c.Blob) == 0 || c.Status != "set" {
		return map[string]string{}, nil
	}
	plain, err := p.vault.Decrypt(c.Blob)
	if err != nil {
		return nil, err
	}
	// A vendor credential is a single secret value. EnvKeys are name aliases
	// for that one value (e.g. a provider that reads either FOO_API_KEY or
	// FOO_KEY), NOT distinct secrets — so each key maps to the same plaintext.
	// See docs/specs/secrets.dog.md "Credential model".
	out := map[string]string{}
	for _, k := range c.EnvKeys {
		if k == "" {
			continue
		}
		out[k] = string(plain)
	}
	return out, nil
}

// PenalizedSnapshotsAll returns the current penalty snapshot for every vendor.
// Used by the web UI status page.
func (p *Pool) PenalizedSnapshotsAll(ctx context.Context) ([]PenalizedSnapshot, error) {
	vs, err := p.db.ListVendors(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]PenalizedSnapshot, 0, len(vs))
	for _, v := range vs {
		pen, _ := p.db.GetPenalty(ctx, v.VendorID)
		out = append(out, PenalizedSnapshot{
			VendorID: v.VendorID, Status: pen.Status, CooldownUntil: pen.CooldownUntil,
		})
	}
	return out, nil
}

// ClearPenalty resets a vendor's penalty box to eligible. Used by the web UI
// "Clear penalty" button. Per spec, for auth_locked vendors, callers should
// also re-save credentials in the same UI session, otherwise the vendor will
// re-lock on next attempt.
func (p *Pool) ClearPenalty(ctx context.Context, vendorID string) error {
	now := p.now()
	return p.db.PutPenalty(ctx, store.Penalty{
		VendorID: vendorID, Status: store.PenaltyEligible, UpdatedAt: now,
	})
}
