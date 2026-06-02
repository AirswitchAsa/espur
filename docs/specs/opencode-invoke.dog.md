# Behavior: OpencodeInvoke

## Condition

A `Trigger` is at the head of its thread queue, the assembled user message exists, and at least one vendor is currently eligible (not in the penalty box) at the top of the priority list.

## Description

For each invocation attempt, Espur shells out to the `opencode` CLI as a child process. The invocation is **stateless** — there is no session reuse, no `--continue`, no shared opencode daemon. Every trigger is its own fresh process.

**Command shape**

```
opencode run --format json --model <vendor-model-id>
```

- `--format json` is required; Espur parses opencode's reply from the JSON envelope on stdout.
- `--model <vendor-model-id>` is taken from the currently-attempted vendor entry in the vendor pool (e.g. `anthropic/claude-sonnet-...`, `openai/gpt-...`, `google/gemini-...`).
- The assembled user message (see [[context-assembly]]) is delivered as opencode's user prompt. The mechanism may be stdin or a positional arg, whichever `opencode run` documents — but it is always the same composite string built by context assembly.

**Working directory**

- Each thread has a dedicated working directory at `data/threads/<thread_id>/`.
- The child process's `cwd` is set to that directory.
- The directory is created on first trigger for the thread (see [[memory-seed]]).
- opencode is given full filesystem tool access scoped to that cwd. Espur does not constrain individual tool calls.

**Environment**

- The minimal env passed to the child: `PATH`, `HOME`, `TMPDIR`, `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME`, and the vendor's credentials in the form opencode expects.
  - For BYO-key vendors that is one or more API-key env vars (e.g. `ANTHROPIC_API_KEY`, `DEEPSEEK_API_KEY`).
  - For OAuth vendors, **no provider-specific env var is injected**. The credential lives in opencode's auth file under `$XDG_DATA_HOME/opencode/auth.json` and is read by opencode itself — see [[oauth]] for the delegation model.
- Espur does **not** leak its own master key or unrelated vendor credentials into the child env. Only the credentials of the vendor currently being attempted are exposed.

**Timeout**

- A wall-clock timeout per invocation. Default **120 seconds**.
- On timeout, the child is killed (`SIGTERM` then `SIGKILL` after a grace period of a few seconds), the attempt is recorded as a timeout, and the timeout reply behavior takes over (see [[reply]]).
- A timeout is **not** counted as a vendor failure — it does not put the vendor in the penalty box and does not cause fallthrough to the next vendor.

**Output parsing — streamed, with an export backstop**

Stdout is opencode's NDJSON event stream, read **line-by-line as the child writes it** (not buffered until exit). Espur reconstructs assistant messages from the events and emits each one the moment it completes, so [[reply]] can post it while the run is still going:

- `step_start` announces a new assistant message (a new `messageID`); `sessionID` is read from the first event that carries it.
- `type=text` events carry the message's text parts. A part streams as repeated events for the same part id (growing text), so text is keyed by part id with last-value-wins and concatenated in first-seen order.
- `step_finish` is the per-message boundary: when it arrives for a message that accumulated non-empty text, that message is emitted. Tool-only and empty messages emit nothing (they are working state, not a reply).

After the child exits, Espur runs `opencode export <sessionID>` **only as a backstop**, to recover a final message whose trailing `type=text` event opencode dropped from stdout (observed intermittently, opencode 1.15.11–1.15.13). The backstop is consulted only when stdout left a gap — no message streamed at all, or the last `messageID` stdout announced never received its text. Crucially:

- The export retry is keyed on that **final messageID**: it waits until the export actually contains that message, rather than accepting the first non-empty read. A fresh `opencode export` right after a run can return a *mid-turn snapshot* — the session is visible but its final message hasn't been written yet (an internal SQLite-visibility delay that grows with session size, observed 4–7s on large turns). Accepting that snapshot would serve an earlier preamble as the answer. Waiting for the announced final messageID prevents that.
- Recovered messages stdout didn't already deliver are emitted in order, after the streamed ones, so the answer arrives last.

Other rules:

- Stderr is captured for diagnostics and used to classify failures (rate limit, quota, 5xx) per [[vendor-pool]].
- A non-zero exit code with no recoverable session is treated as a crash; see [[reply]] for the user-facing message.
- A zero exit code that yields no usable assistant text from either source is also a crash (`no_assistant_text`); a hard export failure when the stream produced nothing is `export_failed`.
- Once a vendor attempt has streamed ≥1 assistant message, a later failure can **not** fall through to another vendor — re-running would replay a second turn over the posted output. Fallthrough applies only to failures that surface before any output (the common auth / rate-limit / 5xx case). See [[vendor-pool]].

**Vendor fallthrough**

- If the invocation's stderr/exit classification matches a fallthrough pattern (see [[vendor-pool]]), Espur immediately re-invokes opencode with the next eligible vendor's `--model` and credentials. The user message and `cwd` are unchanged.
- The new attempt uses a **fresh process** — there is no resumption of opencode state across vendors.

## Outcome

For each accepted trigger, exactly one of the following terminal outcomes is produced:

- **Success** — one vendor returned a parseable assistant reply within timeout. The reply text is handed to [[reply]] for posting.
- **Timeout** — wall clock exceeded; child killed. Hand off to timeout reply path.
- **All drained** — every vendor in the pool was either attempted-and-failed or already in the penalty box. Hand off to all-drained reply path.
- **Crash** — non-classifiable error (e.g. opencode binary missing, malformed JSON, panic). Hand off to error reply path with a request ID.

Side effects of a successful invocation:

- opencode may have created or modified files under the thread's working directory (`AGENTS.md`, `fact_<slug>.md`, etc.). Those changes are kept; Espur does not clean them.
- The transcript is appended to with both the user trigger (already done at enqueue) and the bot's eventual reply (done by [[reply]]).

## Notes

- The child process must inherit no TTY; opencode should detect non-interactive mode from `--format json`.
- The user message is delivered as a **single positional `message` argument** to `opencode run`. `opencode run` documents `[message..]` (a variadic positional). A single argument is sufficient; opencode joins multiple positional args with spaces, which would corrupt the wrapper tags from [[context-assembly]]. Pinned experimentally against opencode 1.15.11.
- The grace period between SIGTERM and SIGKILL on timeout is **5 seconds**. Pinned.
- Global concurrency cap on opencode children: **4**, overridable via `ESPUR_OPENCODE_MAX_CONCURRENT` (set to `0` to disable). Implemented as a buffered semaphore in `internal/vendor/pool.Run`; per-thread serialization (the per-thread queue's job) composes with it. Per-vendor penalty races across concurrent runs are benign by design — see [[vendor-pool]].
- opencode persists every invocation as a session under `~/.local/share/opencode/` (or platform equivalent). Sessions accumulate across invocations and are visible via `opencode session list`. Espur does not currently prune them — to be revisited when this becomes a disk-space concern.
- The vendor-attempt loop is the only retry loop. Within a single vendor attempt, there are no transparent retries by Espur — if opencode itself retries internally that is opaque to us.
