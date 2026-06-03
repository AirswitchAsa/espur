# Behavior: OpencodeInvoke

## Condition

A `Trigger` is at the head of its thread queue, the assembled user message exists, and at least one vendor is currently eligible (not in the penalty box) at the top of the priority list.

## Description

Espur drives the `opencode` agent through its **headless HTTP server** (`opencode serve`) — the same API surface opencode's own TUI uses. Espur runs **one long-lived server per vendor**, started lazily on first use and reused across turns. Each turn is still **stateless**: Espur creates a fresh session, sends the prompt, and never reuses a session or uses `--continue`. The server is the integration surface because the alternative — scraping `opencode run --format json` stdout — is a best-effort stream that intermittently drops the final message's text event and offered no reliable way to stream intermediate messages.

**Why a server per vendor (not one shared, not one per turn)**

- *Per vendor, not shared:* each server's process env carries **only that vendor's credentials** (see Environment). A prompt-injected run in one vendor's session therefore can never read another vendor's key — the same credential-isolation property the old per-invocation child had. A single shared server holding every vendor's key would break it.
- *Persistent, not per-turn:* the server startup cost (low single-digit seconds) is paid once per vendor, not on every turn. There is no background supervisor; health is gated when a turn acquires the server — a dead or never-healthy server is killed and respawned synchronously on the next turn. Servers are killed on shutdown after invocations drain (see [[shutdown]]).

**Server command**

```
opencode serve --port <auto> --hostname 127.0.0.1 --log-level INFO
```

- The port is an OS-assigned free loopback port; the server is never exposed off `127.0.0.1`.
- `--model` is **not** a server flag. The model is chosen per request: Espur splits the vendor's model id `"<provider>/<model>"` on the first slash into `{providerID, modelID}` (e.g. `deepseek/deepseek-v4-pro` → `deepseek`, `deepseek-v4-pro`).

**Per-turn request flow**

1. `POST /session?directory=<thread-cwd>` — the working directory is a per-request query param, so one server serves every thread's cwd. Each thread's dir is `data/threads/<thread_id>/`, created on first trigger (see [[memory-seed]]); opencode has full filesystem tool access scoped to it.
2. Subscribe to the session's events on the server's shared SSE stream (`GET /event`).
3. `POST /session/<id>/message` with the model and the assembled user message (see [[context-assembly]]) as a single text part. This call is **synchronous**: it blocks until the turn goes idle (through any tool calls) and returns the authoritative final assistant message `{info, parts}`.

**Environment**

- The minimal env passed to each server: `PATH`, `HOME`, `TMPDIR`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, plus the vendor's credentials in the form opencode expects.
  - For BYO-key vendors that is one or more API-key env vars (e.g. `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`).
  - For OAuth vendors, **no provider-specific env var is injected**. The credential lives in opencode's auth file under `$XDG_DATA_HOME/opencode/auth.json` and is read by opencode itself — see [[oauth]].
- Espur does **not** leak its own master key or unrelated vendor credentials into a server's env. Only the credentials of the vendor that owns the server are exposed.
- Operators may forward extra env var names via `ESPUR_OPENCODE_ENV_PASSTHROUGH` (comma-separated) for keys consumed by user-installed skills.

**Timeout**

- A wall-clock timeout per invocation. Default **120 seconds**.
- On timeout, Espur calls `POST /session/<id>/abort` to cancel the turn; the attempt is recorded as a timeout and the timeout reply behavior takes over (see [[reply]]). Any messages already streamed stand.
- A timeout is **not** counted as a vendor failure — it does not penalize the vendor and does not cause fallthrough.

**Output — streamed over SSE, reconciled against the authoritative list**

The SSE event bus (`GET /event`) is the real-time channel. Espur reconstructs each assistant message from the events and emits it the moment it completes, so [[reply]] can post it while the run is still going:

- `message.part.updated` carries a message's text part as it grows (repeated events for the same part id; last-value-wins, concatenated in first-seen order, keyed by `messageID`).
- `message.updated` with `info.role == "assistant"` and `info.time.completed` set is the **per-message boundary**: that message's accumulated text, if non-empty and it has a real id, is emitted. Tool-only and empty messages emit nothing (they are working state, not a reply).
- `session.idle` marks the end of the turn; `session.error` carries a structured failure.

The per-session SSE listener is buffered; if it ever overflows it drops an event rather than stalling every other session, and **records that the stream went lossy for this turn**. A dropped event can truncate a live-assembled message (a `message.part.updated` was lost), so on a lossy turn Espur **stops emitting live completions** and lets the reconcile below deliver the full, authoritative text — it never posts a truncated message that the reconcile could not then correct (because that message would already be marked delivered).

After the turn ends, Espur **reconciles** against the authoritative message list (`GET /session/<id>/message`) and emits, in order, any assistant text message the live stream did not already deliver (and captures any error recorded on an authoritative message, in case its `session.error` arrived after the live drain window). This replaces the old `opencode export` backstop entirely: the data is already complete in the server when the turn is idle, so there is **no settle race** to retry around. (If that read fails, Espur falls back to the synchronous response's final message.)

If the invocation context is cancelled before the synchronous send returns — a deadline (timeout) *or* a parent cancellation (shutdown) — Espur aborts the turn on the server and returns whatever already streamed, without reconciling against the now-dead context. Neither path penalizes the vendor.

Other rules:

- Failure detail — a structured `session.error`, a non-2xx send, or an error on the final message — is synthesized into a single string and classified for fallthrough by [[vendor-pool]] (it carries the provider's `statusCode`/message).
- A turn that yields no usable assistant text and no error is a crash (`no_assistant_text`). A server that can't be started or a session that can't be created is a crash (`server_unavailable` / `session_create_failed`).
- Once a vendor attempt has streamed ≥1 assistant message, a later failure can **not** fall through to another vendor — re-running would replay a second turn over the posted output. Fallthrough applies only to failures that surface before any output (the common auth / rate-limit / 5xx case). See [[vendor-pool]].

**Vendor fallthrough**

- If the failure classification matches a fallthrough pattern (see [[vendor-pool]]), Espur immediately retries the turn against the next eligible vendor — i.e. its server, a fresh session, the same user message and cwd. There is no opencode state carried across vendors.

## Outcome

For each accepted trigger, exactly one terminal outcome is produced:

- **Success** — one vendor returned a parseable assistant reply within timeout. The reply text(s) are handed to [[reply]].
- **Timeout** — wall clock exceeded; turn aborted. Hand off to timeout reply path.
- **All drained** — every vendor was either attempted-and-failed or already in the penalty box.
- **Crash** — non-classifiable error. Hand off to error reply path with a request ID.

Side effects of a successful invocation:

- opencode may have created or modified files under the thread's working directory (`memory_index.md`, `<slug>.md` fact files, scratch, etc.). Those changes are kept.
- The transcript is appended with both the user trigger (at enqueue) and the bot's reply (by [[reply]]).

## Notes

- The user message is delivered as a single text **part** in the prompt body — not a positional CLI arg — so the composite wrapper tags from [[context-assembly]] are never re-split.
- Per-vendor servers and their sessions accumulate state in opencode's shared store under `$XDG_DATA_HOME/opencode/`. Espur does not currently prune sessions — to be revisited when disk becomes a concern.
- The vendor-attempt loop is the only retry loop. Within a single attempt there are no transparent retries by Espur.
- Pinned experimentally against opencode 1.15.13: the server emits `message.part.updated` / `message.updated` / `session.idle` (not the `session.next.text.*` family); `POST /session/<id>/message` blocks through tool calls and returns the final message; `directory` is a query param on session create and prompt endpoints.
- The grace period between SIGTERM and SIGKILL when killing a server process group is **5 seconds**.
- Global concurrency cap on concurrent turns: **4**, overridable via `ESPUR_OPENCODE_MAX_CONCURRENT` (`0` disables). Implemented as a buffered semaphore in `internal/vendor/pool.Run`; per-thread serialization composes with it.
