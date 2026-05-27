# Espur — overview

Espur is a single Go binary that:

1. Listens to one or more IM platforms via thin adapters (Discord, WeChat, ...).
2. When @mentioned on a thread, assembles a fresh context and shells out to `opencode run`.
3. Posts opencode's reply back to the thread.
4. Lets opencode maintain its own long-term memory in an `AGENTS.md`-shaped index, scoped per thread.

It is **not** a coding agent for your phone. It is **not** a multi-user SaaS. It is a personal-deploy chat surface for getting work done through opencode from inside IM clients.

This document is the prose tour. The authoritative behavioral contracts live in [`docs/specs/*.dog.md`](specs/) — if this file and a spec disagree, the spec wins.

---

## Trigger

- Espur only acts on a message when **@mentioned** (DM counts as implicit mention).
- One **queue per thread** (channel / group / DM). Messages on the same thread are processed serially. Bursts beyond one queued message are dropped or coalesced with a "still thinking" reply.
- IM webhook retries are deduped by message ID.

See [`specs/trigger.dog.md`](specs/trigger.dog.md), [`specs/adapter.dog.md`](specs/adapter.dog.md).

## Context assembly

For each trigger, Espur builds a fresh opencode invocation. opencode is **stateless** per invocation — there is no persistent opencode session.

The assembled user message contains:

- **Thread context** — last N lines of the channel transcript, verbatim, labelled as recent conversation.
- **Request** — the current incoming message, highlighted as the thing to act on.

The working directory for opencode persists per thread and contains:

- `AGENTS.md` — the memory index, owned and edited by opencode itself.
- `fact_<slug>.md` — detail files written by opencode when something is worth more than a one-liner.
- Any scratch files opencode chooses to keep.

See [`specs/context-assembly.dog.md`](specs/context-assembly.dog.md), [`specs/transcript.dog.md`](specs/transcript.dog.md).

## Memory

Espur seeds each new thread's `AGENTS.md` with instructions telling opencode to:

- Treat the file as a long-term memory index across conversations on this thread.
- Keep entries to **one line each**, in the form `[short title](fact_<slug>.md) — gloss`.
- Write detail to a new `fact_<slug>.md` and add an index entry pointing to it.
- Read detail files on demand via the `read` tool rather than expanding the index.
- Update or remove entries when facts change.

Espur does **not** parse or enforce memory format at runtime. The discipline lives in the seed prompt. If it breaks down in practice, structural enforcement gets bolted on later.

See [`specs/memory-seed.dog.md`](specs/memory-seed.dog.md).

## Vendor pool

- One ordered list of vendors configured via the web UI, e.g. `[chatgpt-oauth, claude-oauth, gemini-api]`.
- Each vendor is the same opencode invocation with the `--model` flag swapped.
- Always start from the **top** of the priority list per trigger.
- On vendor failure, fall through to the next.
- "Failure" means: HTTP 429, "quota exceeded", "usage limit", "high concurrency", persistent 5xx, or opencode-side config errors (unknown provider, model not found).
- A failed vendor enters a **penalty box** (cooldown, exponential backoff with jitter, persisted in SQLite). Subsequent triggers skip vendors currently in the penalty box.
- 401 / 403 puts a vendor in a permanent penalty until reconfigured via the web UI.

See [`specs/vendor-pool.dog.md`](specs/vendor-pool.dog.md), [`specs/opencode-invoke.dog.md`](specs/opencode-invoke.dog.md).

## Reply

- **Batch only.** No streaming. Espur posts one reply when opencode returns. This keeps the cross-platform code identical.
- **All vendors drained** → reply with: *"All vendors exhausted (rate-limited or out of quota). Check the dashboard at `<url>`."* Include which vendors are penalized.
- **Invocation timeout** (default 120s) → reply with a clear timeout message; do not retry automatically.

See [`specs/reply.dog.md`](specs/reply.dog.md).

## Web UI

A small admin UI on a separate port. Scope:

- Configure provider credentials (BYO keys; OAuth flows for ChatGPT / Claude subs are delegated to `opencode auth login` — see [`specs/oauth.dog.md`](specs/oauth.dog.md)).
- Order the vendor priority list.
- See penalty-box state per vendor.
- List threads with their claim status, working-dir size, and last activity.
- Peek at a thread's `AGENTS.md` and recent transcript.

No analytics, no per-thread settings panel (use sensible defaults), no separate logs viewer (use host logs).

See [`specs/webui.dog.md`](specs/webui.dog.md).

## Sandboxing

The deployment unit **is** the sandbox. Run Espur in a container or a small VM. opencode runs as a child process with full tool access (`read`, `write`, `edit`, `bash`) scoped to its working directory. Do not attempt per-invocation Docker.

## Failure modes the user can see

| Scenario | Reply |
| -------- | ----- |
| All vendors drained | Plain message naming the drained vendors, link to dashboard |
| Timeout | "Took too long, aborted. Try again or rephrase." |
| opencode crash | "Internal error. Check logs." (with a request ID) |
| Auth failure on selected vendor | Silent fallthrough to next vendor; if all auth-failed, treat as drained |
| Memory file write conflict | Should not happen — per-thread queue prevents concurrent writes |
