# Behavior: Observability

## Condition

Espur is running. The operator needs to (a) confirm the system is healthy, (b) diagnose user-reported issues, and (c) correlate user-visible error replies to internal failures. There is **no separate logs viewer** in the web UI — host logs (container stdout/stderr, journald, whatever the deploy uses) are the operator's tool.

## Description

**Logs are the primary observability surface.**

- All logs are written to **stdout** as structured records (one JSON object per line). No file-based logging, no log rotation inside Espur.
- The container runtime / supervisor is responsible for collection. Espur does not assume a particular log backend.
- The default level is `info`; `ESPUR_LOG_LEVEL` (per [[bootstrap]]) accepts `debug`, `info`, `warn`, `error`.

**Required fields on every record.**

- `ts` — RFC3339 UTC.
- `level` — `debug` / `info` / `warn` / `error`.
- `event` — a short stable identifier like `trigger.accepted`, `vendor.cooldown.entered`, `adapter.reconnecting`, `boot.ready`, `shutdown.complete`. New events are added freely; renames count as a breaking change to log consumers and need the same care as a schema change.
- `msg` — a short human-readable sentence. Optional if `event` is self-explanatory.

**Conditional fields.**

- `platform` — present on any log line that originates from an [[adapter]] or that handles an adapter event.
- `thread_id` — encoded form, present on any log line tied to a specific thread.
- `vendor_id` — present on vendor-pool transitions and opencode invocation outcomes.
- `request_id` — present on:
  - Every `error`-level log line emitted during an invocation that ultimately failed with a non-success [[reply]] outcome.
  - The corresponding `kind=bot` [[transcript]] record (`meta.request_id`).
  - The user-visible reply itself for `crash` and `drained` outcomes.
  Same value in all three places. This is the join key the operator uses to walk from "user complained" to "what actually happened."
- `attempt` — present on per-attempt retry / reconnect lines.

**What is never logged.**

- Message bodies (user inbound, bot outbound) — never, at any level.
- Credential values, OAuth tokens, master key bytes — never, at any level.
- Webhook payload bodies — only size and first 200 bytes' worth of structural metadata, and only at `warn`/`error`.
- `AGENTS.md` / `fact_*.md` contents — never. The web UI peek is the surface for that.

Author labels, ids, message ids, vendor ids, thread ids (encoded), counts, durations, error categories — all OK.

**Levels — discipline, not a free-for-all.**

- `debug` — verbose per-step traces. Off by default. Safe to enable in dev; in prod it raises log volume sharply.
- `info` — state transitions an operator wants to see in steady state: boot.ready, trigger.accepted, invocation.success, vendor.cooldown.entered, adapter.connected, shutdown.complete. Bounded volume — roughly proportional to user activity, not per-token or per-chunk.
- `warn` — recoverable anomalies: backpressure drop, transient transport blip, malformed webhook payload, partial chunk delivery, refresh-token rotation succeeded but late.
- `error` — anything that produced a user-visible non-success [[reply]] outcome, plus boot/shutdown failures and master-key issues. An error log always carries enough context to find the corresponding transcript record or user reply.

**Counters / metrics.**

- No Prometheus / OpenTelemetry surface in v0.1. The web UI status page derives a small set of counters (in-flight invocations, vendors by penalty state, threads by activity) from in-memory state for display, not for scraping.
- If metrics become necessary, they get bolted on as a separate spec; the observability contract here does not have to change.

**Health.**

- `Healthy()` on each [[adapter]] is exposed in the web UI status panel.
- The web UI also exposes `GET /healthz` returning JSON `{ok, adapters:[{platform, healthy}]}`. 200 when all registered adapters are healthy, 503 otherwise. Body shape is stable across both status codes so scrapers can parse one way. This endpoint is the only path on the admin port that an orchestrator / reverse proxy is expected to hit unauthenticated; deploys SHOULD allow it past auth.
- Liveness from the container runtime's perspective is "the process is running"; Espur does not need a self-kill watchdog.

**Request-ID lifecycle.**

- A `request_id` is generated lazily, the first time during a trigger's processing that a non-success path is taken (timeout, crash, drained, or a partial-chunk post failure). Success paths do not produce a `request_id` and don't need one — they are already correlated by `(thread_id, ts)` between the transcript and the logs.
- Format: 8-character Crockford base32 (e.g. `XK4Q7B9R`).
- The same `request_id` appears in: every log line emitted during the remaining lifetime of that trigger's processing, the [[transcript]] `kind=bot` record (and, for shutdown-aborted turns, the `kind=system` record), and the user-visible reply if the outcome is `crash` or `drained`.

**What about the operator finding "why did vendor X go into cooldown at 14:32"?**

- They search host logs for `event=vendor.cooldown.entered vendor_id=X` and find one structured line with `attempt`, `failure_category`, `cooldown_until`, and (if relevant) the `request_id` of the trigger that caused it. From that they can find the full trigger trace.

## Outcome

For every user-reported issue, the operator can:

- Recover the trigger that caused it from the [[transcript]] record (`request_id` shown in the reply maps to a transcript line).
- Recover the corresponding log lines (search host logs by `request_id`).
- See the full state transitions of the trigger: which vendor was tried, why it failed, whether the cooldown / auth-lock was entered, what the eventual reply outcome was.

For steady-state observation, host logs alone give a complete picture without needing any additional tooling, and the web UI status page surfaces the things an operator wants to glance at without parsing logs (adapters up/down, vendors penalized/eligible, threads active).

No user content, no credentials, and no secret material ever leave Espur via logs.

## Notes

- Decided: log records are JSON, one object per line, written to stdout (trivial container-log parsing).
- Decided: at `info`, one record per accepted trigger (`event=trigger.accepted`) and one per invocation outcome (`event=invocation.success` or the matching failure); nothing finer-grained at `info`.
- Decided: the event-name registry is a flat constants file in code (`internal/obs/events.go`); the spec does not mirror it line-by-line.
- The deliberate omission of OpenTelemetry, Prometheus, distributed tracing, and a built-in log viewer is a v0.1 simplicity choice. Spec extension is the path forward if any of those become needed.
