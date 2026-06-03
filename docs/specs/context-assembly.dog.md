# Behavior: ContextAssembly

## Condition

A `Trigger` has reached the head of its thread queue and is about to be handed to opencode. The thread has a working directory (seeded per [[memory-seed]]) and a transcript log (possibly empty for a brand-new thread).

## Description

Espur builds the single user message passed to the opencode session as the request (see [[opencode-invoke]]). The message is laid out as a **stable cache-friendly prefix** followed by **volatile content**, so an upstream prompt cache can match the leading bytes across turns on the same thread:

### Stable prefix

1. **Thread identity** — a `<thread platform="…" id="…">` wrapper carrying the platform and the raw (un-encoded) thread id. Attribute values are escaped (`"` → `&quot;`, `\` → `\\`).

2. **Inlined memory** — the full contents of the thread's `AGENTS.md` (the memory instructions + the operator custom-instructions block — see [[memory-seed]]) wrapped in a `<memory>` block:

   ```
   <memory note="AGENTS.md for this thread; the memory_index.md index and its <slug>.md fact files live alongside it">
   …AGENTS.md bytes…
   </memory>
   ```

   This is what guarantees the memory **instructions** and the operator persona are in context on every turn without depending on opencode re-reading the file. It is capped at 16 KiB (`MaxAgentsMDBytes`); beyond that the content is truncated as a runaway guard. `AGENTS.md` is the *only* memory file inlined — `memory_index.md` and the `<slug>.md` detail files are **not** inlined; they stay read-on-demand via the agent's tools in the session's working directory.

   These two pieces are byte-stable for a given thread (identity never changes; `AGENTS.md` changes only when the agent rewrites it or the operator edits the instructions block), which is what lets the prefix land a prompt-cache hit.

### Volatile suffix

3. **Thread context** — the last N **user messages** from the thread transcript (see [[transcript]]), verbatim, oldest first, wrapped in `<thread-context>`:

   ```
   <thread-context note="recent user messages on this thread, oldest first">
   alice: previous message
   alice: another message
   bob: a third party also chimed in
   </thread-context>
   ```

   Only `kind = user` records appear here. Bot replies and system records are filtered out — opencode sees what the user(s) said, not its own prior outputs. Coalesced-away user messages **are** included — they were said (their supersession is recorded separately as a `kind = system` back-pointer, which is filtered out here; see [[transcript]]). Each record is rendered by `transcript.Format` (author label + body), preserving newlines within a message.

4. **Request** — the current `Trigger.text`, the message to act on now:

   ```
   <request from="alice">
   the current incoming message text
   </request>
   ```

   The current trigger is **not** duplicated into the thread-context block — context is "prior" by definition. If the author label is empty it renders as `user`.

opencode is stateless across invocations (no persistent session, no `--continue` — see [[opencode-invoke]]). Espur builds this composite message fresh on every trigger. The thread's chat history is delivered only through the `<thread-context>` tail above and whatever durable memory the agent has chosen to write into its working directory.

## Outcome

A single string is produced and passed as the user message to the opencode session. In order it contains:

- A `<thread …>` identity wrapper enclosing a `<memory>` block with the inlined `AGENTS.md` (when non-empty).
- A `<thread-context>` block with the most recent user-message tail (up to N records of `kind = user`, fewer if the thread is short).
- A `<request>` block with the current incoming message text and author label.

No other content is in the user message.

## Notes

- N (transcript-tail length) defaults to **15** (`DefaultTailN`), and the same value drives the admin UI's transcript peek.
- The thread-context block is byte-capped at **8 KiB** (`MaxBytes`); on overflow the oldest lines are dropped (rounded to a line boundary so a line is never split). The current request is appended *after* the cap check, so it is always preserved verbatim.
- The inlined `AGENTS.md` is capped separately at **16 KiB** (`MaxAgentsMDBytes`).
- The transcript itself (storage, append, tail read) is described in [[transcript]] — context assembly is a pure read of the tail.
- opencode also reads `AGENTS.md` from the session working directory by its own convention, so the inlining is partly belt-and-suspenders; the inlining is what makes the bytes part of the *cacheable prefix* and independent of opencode's file-loading behavior.
- Attachments, images, embeds: out of scope for the first cut. Adapters render them to a placeholder text token in the transcript.
- The wrapper tag names (`thread`, `memory`, `thread-context`, `request`) are an implementation choice but are kept stable so opencode behavior is reproducible and the cache prefix is consistent.
