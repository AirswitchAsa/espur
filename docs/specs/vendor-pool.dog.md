# Behavior: VendorPool

## Condition

A trigger is ready to invoke opencode and the vendor pool is consulted to choose which vendor to attempt next. The vendor pool is consulted on every trigger and on every fallthrough within a trigger.

## Description

**Configuration**

- A single ordered list of vendor entries, owned by the admin web UI and persisted in SQLite.
- Each entry has:
  - A stable `vendor_id` (e.g. `chatgpt-oauth`, `claude-oauth`, `gemini-api`).
  - A `model` string used as opencode's `--model` flag value.
  - Credentials (BYO API key, or OAuth tokens for ChatGPT/Claude subscription accounts), stored encrypted via the secrets layer.
  - An `enabled` flag.
- Order in the list **is** the priority. The first entry is the most preferred vendor.

**Selection — always from the top**

- On every new trigger, iteration starts at index 0 of the enabled list.
- The first vendor whose penalty-box state is `eligible` is attempted.
- Espur does **not** track "last used" or rotate. There is no round-robin. A healthy top vendor is used for every trigger until it fails.

**Failure detection**

A vendor attempt is classified as a **failure** (eligible for fallthrough + penalty box) when any of these are detected, by inspecting opencode's stderr, exit code, and any structured error fields in its JSON output:

- HTTP **429** from the upstream API.
- Substring matches (case-insensitive) for vendor-side throttling: `quota exceeded`, `usage limit`, `rate limit`, `high concurrency`, `overloaded`, `try again later`.
- Persistent **5xx** from the upstream API (any 5xx counts; a single 5xx is enough to trigger fallthrough, but only escalates the penalty on repeat — see backoff below).
- HTTP **401** or **403** from the upstream API, **or** any opencode error string clearly indicating auth (`invalid api key`, `unauthorized`, `expired token`, `revoked`), **or** an opencode-side configuration error that is structurally equivalent — `model not found`, `unknown provider`, `provider not configured`. All of these mean the vendor row is unusable until the operator intervenes (re-auth, fix model id, re-add credentials), so they share the auth-failure bucket and the `auth_locked` permanent penalty.

Patterns are seeded from the `opencode-rate-limit-fallback` plugin source and may be extended over time. The full pattern list lives in code, not in this spec.

A failure causes:

1. The vendor enters the penalty box (rules below).
2. Espur immediately falls through to the next eligible vendor in priority order and tries again, without informing the user.

**Not a failure** (do not penalize, do not fall through):

- Wall-clock timeout (the invocation timeout — see [[opencode-invoke]]).
- Successful response with empty text (treated as a crash by [[opencode-invoke]], not a vendor fault).
- User-content errors (model refused, returned an error message as content).

**Penalty box**

- Persisted in SQLite so it survives restarts.
- Per-vendor state:
  - `status` — one of `eligible`, `cooldown`, `auth_locked`.
  - `failure_streak` — count of consecutive failures of the rate-limit / quota / 5xx class.
  - `cooldown_until` — absolute timestamp; while `now < cooldown_until`, the vendor is skipped.
- On a rate-limit / quota / 5xx classified failure:
  - `failure_streak += 1`.
  - `status = cooldown`.
  - `cooldown_until = now + backoff(failure_streak)`, where `backoff` is exponential with jitter:
    - Base 30 seconds, doubling each step: 30s, 60s, 2m, 4m, 8m, 16m, 32m, capped at 1 hour.
    - Multiply by a uniform random factor in `[0.5, 1.5]` for jitter.
- On a **401 / 403 / auth-class** failure:
  - `status = auth_locked` (a permanent penalty).
  - `cooldown_until` is ignored while `auth_locked` — only reconfiguration via the web UI (saving new credentials for that vendor, or explicitly clearing the lock) returns the vendor to `eligible`.
- On a **successful** invocation for a vendor:
  - `failure_streak = 0`.
  - `status = eligible`.
  - `cooldown_until = null`.
- Cooldown expires lazily: when the vendor is next consulted and `now >= cooldown_until`, it is treated as `eligible` for that attempt. A failed re-attempt re-enters cooldown at the next backoff step (does not reset the streak unless the attempt succeeded).

**All-drained behavior**

- If the iteration walks the entire enabled list and finds no eligible vendor (every vendor is in `cooldown` or `auth_locked`), the trigger ends with the **all-drained** terminal outcome — no opencode invocation is attempted at all.
- The user-visible reply is described in [[reply]] and must enumerate which vendors are currently penalized and why (cooldown vs. auth).

## Outcome

For any given trigger, the vendor pool yields one of:

- A concrete `(vendor_id, model, credentials)` to attempt next, drawn from the top of the priority list among eligible vendors.
- An **all-drained** signal, when no eligible vendor exists, ending the invocation phase before any opencode child is launched (or after the last fallthrough failure consumes the final eligible vendor).

Penalty-box state is updated on every attempt outcome (success, classified failure, or auth failure) and persisted before the next trigger is processed.

Timeouts and crashes do not mutate penalty-box state.

## Notes

- Decided: backoff steps are 30s, 60s, 2m, 4m, 8m, 16m, 32m, capped at 1h, indexed by failure streak, with uniform ±50% jitter.
- Decided: a single 5xx triggers cooldown immediately (no N-in-a-row threshold); it just starts at the shortest backoff step.
- Decided: on cooldown expiry the vendor returns to plain `eligible` (lazily, on next consult) — no half-open probe state.
- Decided: both clears exist — a manual "clear penalty" button in the web UI and the implicit clear on credential re-save. See [[webui]].
- A vendor toggled `enabled = false` in the web UI is skipped regardless of penalty state and does not consume its slot. Re-enabling does not reset the penalty box; status stays as last persisted.
- The pattern list for failure classification lives in code (Go constants / regexps) so it can evolve without spec churn. The spec only fixes the **categories** (429, quota/limit phrases, 5xx, auth).
