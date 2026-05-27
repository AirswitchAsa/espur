package vendor

import (
	"math/rand"
	"time"

	"github.com/punny/espur/internal/store"
)

// BackoffSteps are the cooldown durations applied by failure_streak.
// Spec: vendor-pool.dog.md — 30s, 60s, 2m, 4m, 8m, 16m, 32m, capped at 1h.
var BackoffSteps = []time.Duration{
	30 * time.Second,
	60 * time.Second,
	2 * time.Minute,
	4 * time.Minute,
	8 * time.Minute,
	16 * time.Minute,
	32 * time.Minute,
	60 * time.Minute,
}

// backoffFor returns the cooldown duration for the (1-indexed) streak value,
// with uniform ±50% jitter per spec.
func backoffFor(streak int, rng *rand.Rand) time.Duration {
	if streak < 1 {
		streak = 1
	}
	i := streak - 1
	if i >= len(BackoffSteps) {
		i = len(BackoffSteps) - 1
	}
	base := BackoffSteps[i]
	jitter := 0.5 + rng.Float64() // [0.5, 1.5)
	return time.Duration(float64(base) * jitter)
}

// applyFailure mutates penalty for a classified failure. ClassAuth → permanent
// auth_locked; ClassRateLimit / ClassServer5xx → cooldown w/ exponential backoff.
func applyFailure(p store.Penalty, class FailureClass, now time.Time, rng *rand.Rand) store.Penalty {
	switch class {
	case ClassAuth:
		p.Status = store.PenaltyAuthLocked
		p.CooldownUntil = nil
	case ClassRateLimit, ClassServer5xx:
		p.FailureStreak++
		p.Status = store.PenaltyCooldown
		until := now.Add(backoffFor(p.FailureStreak, rng))
		p.CooldownUntil = &until
	}
	p.UpdatedAt = now
	return p
}

func applySuccess(p store.Penalty, now time.Time) store.Penalty {
	p.FailureStreak = 0
	p.Status = store.PenaltyEligible
	p.CooldownUntil = nil
	p.UpdatedAt = now
	return p
}

// isEligible reports whether p is currently eligible at the given instant.
// Cooldown lapses lazily on consult per spec.
func isEligible(p store.Penalty, now time.Time) bool {
	switch p.Status {
	case store.PenaltyAuthLocked:
		return false
	case store.PenaltyCooldown:
		return p.CooldownUntil == nil || !now.Before(*p.CooldownUntil)
	default:
		return true
	}
}
