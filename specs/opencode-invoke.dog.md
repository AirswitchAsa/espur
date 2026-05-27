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

- The minimal env passed to the child: `PATH`, `HOME`, `TMPDIR`, and the vendor's credentials in the form opencode expects (API keys via env vars; OAuth tokens via opencode's auth file mechanism for that vendor).
- Espur does **not** leak its own master key or unrelated vendor credentials into the child env. Only the credentials of the vendor currently being attempted are exposed.

**Timeout**

- A wall-clock timeout per invocation. Default **120 seconds**.
- On timeout, the child is killed (`SIGTERM` then `SIGKILL` after a grace period of a few seconds), the attempt is recorded as a timeout, and the timeout reply behavior takes over (see [[reply]]).
- A timeout is **not** counted as a vendor failure — it does not put the vendor in the penalty box and does not cause fallthrough to the next vendor.

**Output parsing**

- Stdout is treated as opencode's NDJSON event stream. Espur reads the `sessionID` field from the first emitted event (typically `step_start`).
- After the child exits, Espur runs `opencode export <sessionID>` and reads the **assistant message's `text`-type parts** from the returned session record. Their concatenation is the assistant reply.
- Rationale: as of opencode 1.15.11, the NDJSON stdout stream intermittently omits trailing `type=text` events even when the session itself contains the assistant text. The session export is the authoritative source.
- Stderr is captured for diagnostics and used to classify failures (rate limit, quota, 5xx) per [[vendor-pool]].
- A non-zero exit code with no recoverable session is treated as a crash; see [[reply]] for the user-facing message.
- A zero exit code with no usable assistant text in the exported session is also a crash (same path).

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
- TODO(decision): max concurrent opencode child processes across all threads. README implies per-thread serialization but not a global cap. Suggest a configurable global concurrency limit (default e.g. 4) to bound resource use on small hosts; confirm.
- opencode persists every invocation as a session under `~/.local/share/opencode/` (or platform equivalent). Sessions accumulate across invocations and are visible via `opencode session list`. Espur does not currently prune them — to be revisited when this becomes a disk-space concern.
- The vendor-attempt loop is the only retry loop. Within a single vendor attempt, there are no transparent retries by Espur — if opencode itself retries internally that is opaque to us.
